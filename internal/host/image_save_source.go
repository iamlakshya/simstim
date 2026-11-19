package host

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/paths"
)

// ImageSaveSource can only originate from an actual confirmed native runtime,
// its frozen boot input and a new retained primary operation. It exposes no
// raw root, Need, state, registry or path getter.
type ImageSaveSource struct {
	ctx             context.Context
	cancel          context.CancelFunc
	originalContext context.Context
	receipt         *OriginalVMNeed
	boot            *imageSaveBootV1
	operation       *SessionOperation
	connection      net.Conn
	domain          cachefs.Domain
	generation      string
	values          map[string]api.Secret
	names           map[string]bool
	mu              sync.Mutex
	active          sync.WaitGroup
	preparing       bool
	closing         atomic.Bool
	store           *image.Store
	publication     *runtimePublication
	pending         *image.PreparedSave
	once            sync.Once
	closeErr        error
}

// CaptureImageSaveSource transfers operation only on success. ctx is the
// original accepted connection lifetime, not a single request's reply scope.
// Runtime must cancel and physically join its guest layer reservation before
// calling Close; Prepare also joins its actual producer before it returns.
func CaptureImageSaveSource(ctx, originalRuntimeContext context.Context, operation *SessionOperation, exactStartConnection net.Conn, originalPublic *api.SessionSecretState, originalDecoys []api.DecoyTripwire) (*ImageSaveSource, error) {
	if ctx == nil || originalRuntimeContext == nil || ctx.Err() != nil || originalRuntimeContext.Err() != nil || operation == nil || exactStartConnection == nil {
		return nil, imageSaveSourceFailure()
	}
	receipt := OriginalVMNeedFromContext(originalRuntimeContext)
	boot, _ := originalRuntimeContext.Value(imageSaveBootKeyV1{}).(*imageSaveBootV1)
	if receipt == nil || boot == nil || boot.boot != receipt.boot || receipt.need == nil || operation.Primary() != receipt.boot.loan.Primary {
		return nil, imageSaveSourceFailure()
	}
	domain, err := OriginalBootDomain(originalRuntimeContext)
	if err != nil {
		return nil, err
	}
	owned, cancel := context.WithCancel(ctx)
	source := &ImageSaveSource{ctx: owned, cancel: cancel, originalContext: originalRuntimeContext, receipt: receipt, boot: boot, operation: operation, connection: exactStartConnection, domain: domain}
	if err := source.checkNative(ctx); err != nil {
		cancel()
		return nil, err
	}
	manifest, err := operation.LoadContext(ctx)
	if err != nil {
		cancel()
		return nil, err
	}
	if manifest.Wall == nil || manifest.Wall.Level != "microvm" {
		cancel()
		return nil, UserError("image_wall", "image save needs a running microvm sandbox", "Start a microvm sandbox, set up its tools, then save it.")
	}
	if manifest.Life == nil || manifest.Life.Phase != LifeRunning {
		cancel()
		return nil, UserError("sandbox_stopped", "the sandbox is not running", "Resume its microvm before saving its image.")
	}
	if manifest.ID != operation.Primary() || manifest.SandboxOf != "" || manifest.Run.Generation != receipt.boot.owner.generation || manifest.Run.HolderPID != os.Getpid() || !bytes.Equal(manifest.VMNeedV1, receipt.rawNeed) {
		cancel()
		return nil, imageSaveSourceFailure()
	}
	source.generation = manifest.Run.Generation
	members, err := SandboxMembers(manifest.ID)
	if err != nil {
		cancel()
		return nil, err
	}
	source.values, source.names, err = imageSaveScanUnion(manifest, members, originalPublic, originalDecoys)
	if err != nil {
		cancel()
		return nil, err
	}
	if err := source.Check(ctx); err != nil {
		clear(source.values)
		cancel()
		return nil, err
	}
	return source, nil
}

// Check proves only this same source. It never reloads policy or derives a
// replacement snapshot from later metadata.
func (source *ImageSaveSource) Check(ctx context.Context) error {
	if source == nil || source.closing.Load() || source.ctx == nil || source.ctx.Err() != nil || ctx == nil || ctx.Err() != nil {
		return imageSaveSourceFailure()
	}
	return source.checkNative(ctx)
}

func (source *ImageSaveSource) checkNative(ctx context.Context) error {
	if source == nil || source.originalContext == nil || source.receipt == nil || OriginalVMNeedFromContext(source.originalContext) != source.receipt || source.operation == nil || !source.operation.Valid(source.receipt.boot.loan.Primary) {
		return imageSaveSourceFailure()
	}
	receipt := source.receipt
	receipt.prepareMu.Lock()
	defer receipt.prepareMu.Unlock()
	owner := receipt.boot.owner
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.startup != receipt.boot || !owner.Valid(receipt.boot.loan.Primary) || !receipt.boot.ready || !receipt.prepareConfirmed || receipt.binding == nil || receipt.connection == nil || !bytes.Equal(receipt.boot.currentNeed, receipt.rawNeed) || source.generation != "" && source.generation != owner.generation {
		return imageSaveSourceFailure()
	}
	if err := receipt.binding.Check(ctx, receipt.connection, *receipt.need); err != nil {
		return err
	}
	return receipt.binding.Check(ctx, source.connection, *receipt.need)
}

// Prepare derives all exclusion and scan input from this source. The caller's
// request cannot replace its boot state, PATH, stand-ins or original host.
func (source *ImageSaveSource) Prepare(ctx context.Context, request image.SaveRequest, changes []string, writeRoot func(context.Context, io.Writer) error) (*image.PreparedSave, error) {
	if source == nil || ctx == nil || writeRoot == nil {
		return nil, imageSaveSourceFailure()
	}
	if err := source.Check(ctx); err != nil {
		return nil, err
	}
	source.mu.Lock()
	if source.closing.Load() || source.preparing || source.pending != nil {
		source.mu.Unlock()
		return nil, UserError("sandbox_busy", "this original image source already has a save", "Finish its review before saving again.")
	}
	source.preparing = true
	source.active.Add(1)
	source.mu.Unlock()
	defer func() { source.mu.Lock(); source.preparing = false; source.mu.Unlock(); source.active.Done() }()
	owned, finish := imageSaveContext(ctx, source.ctx)
	defer finish()
	if err := source.Check(owned); err != nil {
		return nil, err
	}
	values := make(map[string]api.Secret, len(source.values)+len(request.Values))
	for name, value := range source.values {
		values[name] = value
	}
	defer clear(values)
	for name, value := range request.Values {
		if api.CheckSecretName(name, false) != api.SecretNameOK || !source.names[name] {
			return nil, imageSaveSourceFailure()
		}
		values[name] = value
	}
	plan, err := image.NewSavedRootPlan(source.boot.original, source.boot.actual, source.boot.path, values, changes)
	if err != nil {
		return nil, err
	}
	defer clear(plan.Values)
	if err := source.openStore(owned); err != nil {
		return nil, err
	}
	owned, finishPublication := imageSaveContext(owned, source.publication.ctx)
	defer finishPublication()
	prepared, err := source.store.PrepareSave(owned, source.publication.ctx, request, plan, func(ctx context.Context, output io.Writer) error {
		if err := source.Check(ctx); err != nil {
			return err
		}
		if err := writeRoot(ctx, output); err != nil {
			return err
		}
		return source.Check(ctx)
	}, ConvertSavedImage)
	if err != nil {
		return nil, err
	}
	if err := source.Check(owned); err != nil {
		return nil, errors.Join(err, prepared.Close())
	}
	source.mu.Lock()
	source.pending = prepared
	source.mu.Unlock()
	return prepared, nil
}

func (source *ImageSaveSource) openStore(ctx context.Context) error {
	if source.store != nil {
		return nil
	}
	publication, err := beginImageSavePublication(source.ctx, source)
	if err != nil {
		return err
	}
	if publication.owner.domain() != source.domain {
		return errors.Join(imageSaveSourceFailure(), publication.close())
	}
	store, err := image.OpenHost(ctx, paths.ImagesDir(), source.domain, publication.gate)
	if err != nil {
		return errors.Join(err, publication.close())
	}
	source.store, source.publication = store, publication
	return nil
}

// Close cancels and joins Prepare, expiry and owned local descriptors before
// releasing this exact original OP. Runtime joins guest I/O before this call.
func (source *ImageSaveSource) Close() error {
	if source == nil {
		return nil
	}
	if source.ctx == nil || source.cancel == nil || source.operation == nil {
		return imageSaveSourceFailure()
	}
	source.once.Do(func() {
		source.mu.Lock()
		source.closing.Store(true)
		source.cancel()
		source.mu.Unlock()
		source.active.Wait()
		if source.pending != nil {
			source.closeErr = errors.Join(source.closeErr, source.pending.Close())
		}
		if source.store != nil {
			source.closeErr = errors.Join(source.closeErr, source.store.Close())
		}
		if source.publication != nil {
			source.closeErr = errors.Join(source.closeErr, source.publication.close())
		}
		clear(source.values)
		source.closeErr = errors.Join(source.closeErr, source.operation.CloseWithError())
	})
	return source.closeErr
}

func imageSaveSourceFailure() error {
	return UserError("sandbox_changed", "the original running image-save source is unavailable or changed", "Use the current microvm session through its original accepted connection.")
}

func imageSaveContext(ctx, lifetime context.Context) (context.Context, func()) {
	owned, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	stop := context.AfterFunc(lifetime, func() { defer close(done); cancel() })
	return owned, func() {
		if !stop() {
			<-done
		}
		cancel()
	}
}

func beginImageSavePublication(ctx context.Context, source *ImageSaveSource) (*runtimePublication, error) {
	if err := source.Check(ctx); err != nil {
		return nil, err
	}
	operation, err := beginPlacementOperation(ctx, "publish")
	if err != nil {
		return nil, err
	}
	owned, cancel := context.WithCancelCause(ctx)
	publication := &runtimePublication{ctx: owned, cancel: cancel, owner: operation.owner, operation: operation, stop: make(chan struct{}), watched: make(chan struct{}), imageSave: source}
	go publication.watch()
	return publication, nil
}
