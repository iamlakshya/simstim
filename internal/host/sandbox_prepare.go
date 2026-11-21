package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/vmneed"
	"github.com/iamlakshya/simstim/internal/wall/gvisor"
)

const (
	sandboxPrepareLabelKeyV1   = "sandbox_prepare_v1"
	sandboxPrepareDiskStringV1 = "1G"
)

// sandboxPrepareTimeoutV1 bounds the RPC result and cancels construction.
// Its original request worker retains unresolved native cleanup after the
// failure response; the response cannot release the operation or its resources.
var sandboxPrepareTimeoutV1 = time.Duration(api.SandboxPrepareTimeoutSecondsV1) * time.Second

type sandboxPrepareWitnessV1 struct {
	mu          sync.Mutex
	bridge      *bridge
	requestID   uint64
	hostID      string
	level       string
	driver      string
	raw         []byte
	encoded     []byte
	needBytes   []byte
	need        *vmneed.NeedV1
	runtime     *selectedRuntimeReceiptV1
	agents      *InstalledAgentsReceiptV1
	imageLease  *image.Lease
	imageSize   int64
	agentsSize  int64
	captured    bool
	bound       *SessionOperation
	reservation *sessionReservation
	parentCheck cachefs.Reference // comparison only; the retained native parent grants custody
	written     bool
}

func (witness *sandboxPrepareWitnessV1) releaseImageLease() error {
	if witness == nil {
		return nil
	}
	witness.mu.Lock()
	lease := witness.imageLease
	witness.imageLease = nil
	witness.mu.Unlock()
	if lease == nil {
		return nil
	}
	return lease.Close()
}

func sandboxBootError(message, hint string) *proto.Error {
	if strings.TrimSpace(hint) == "" {
		hint = "Run " + brand.Name + " host doctor and check the host's sandbox setup before retrying."
	}
	return UserError("sandbox_boot", message, hint)
}

func wrapSandboxBootError(err error, fallbackHint string) error {
	if err == nil {
		return nil
	}
	var kept *keptError
	if errors.As(err, &kept) && kept != nil && kept.err != nil {
		hint := kept.err.Hint
		if strings.TrimSpace(hint) == "" {
			hint = fallbackHint
		}
		return &keptError{err: sandboxBootError(kept.err.Message, hint)}
	}
	var pe *proto.Error
	if errors.As(err, &pe) && pe != nil {
		hint := pe.Hint
		if strings.TrimSpace(hint) == "" {
			hint = fallbackHint
		}
		return sandboxBootError(pe.Message, hint)
	}
	return sandboxBootError("could not complete the sandbox test boot: "+err.Error(), fallbackHint)
}

// IsSandboxPrepareManifestV1 reports whether m carries the closed fixed
// test-boot marker.
func IsSandboxPrepareManifestV1(m *Manifest) bool {
	return m != nil && m.Labels != nil && m.Labels[sandboxPrepareLabelKeyV1] == "1"
}

func (b *bridge) handleSandboxPrepareV1(req proto.Request) {
	if err := CheckCaller(); err != nil {
		b.reply(req.ID, nil, err)
		return
	}
	if b == nil || b.ctx == nil || req.ID == 0 || req.Op != "sandbox.prepare" {
		b.reply(req.ID, nil, UserError("bad_request", "the sandbox prepare request has no authenticated bridge owner", "Open a fresh host connection and run host prepare again."))
		return
	}
	raw := req.Args
	var args api.SandboxPrepareArgsV1
	if len(raw) == 0 || !strings.HasPrefix(strings.TrimSpace(string(raw)), "{") ||
		!api.ExactJSONFields(raw, args) || decodeRuntime(raw, &args) != nil || args.Version != 1 {
		b.reply(req.ID, nil, UserError("bad_request", "the sandbox prepare arguments are not a supported version 1 object", "Update the client and host helper, then run host prepare again."))
		return
	}
	prepareDeadline := time.Now().Add(sandboxPrepareTimeoutV1)
	ctx, cancel := context.WithDeadline(b.ctx, prepareDeadline)
	defer cancel()
	response := newSandboxPrepareResponseV1(b, req.ID)
	responseDone := make(chan struct{})
	stopResponse := context.AfterFunc(ctx, func() {
		defer close(responseDone)
		response.reply(api.SandboxPrepareResultV1{}, sandboxBootError(
			"the sandbox test boot was cancelled or reached its time limit; physical cleanup is not yet confirmed",
			"Keep the original host helper running while it joins its owned cleanup. Check the host doctor before retrying.",
		))
	})
	defer func() {
		if !stopResponse() {
			<-responseDone
		}
	}()

	witness, err := b.newSandboxPrepareWitnessV1(ctx, req, raw, args)
	if err != nil {
		response.reply(api.SandboxPrepareResultV1{}, err)
		return
	}
	defer func() { _ = witness.releaseImageLease() }()

	result, err := b.runSandboxPreparePlatformV1(ctx, prepareDeadline, witness)
	response.reply(result, err)
}

func (b *bridge) newSandboxPrepareWitnessV1(ctx context.Context, req proto.Request, raw json.RawMessage, args api.SandboxPrepareArgsV1) (_ *sandboxPrepareWitnessV1, failure error) {
	if err := errors.Join(CheckCaller(), ctx.Err(), b.ctx.Err()); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	witness := &sandboxPrepareWitnessV1{
		bridge:    b,
		requestID: req.ID,
		hostID:    b.hostID,
		level:     args.Level,
		raw:       bytes.Clone(raw),
		encoded:   encoded,
		needBytes: bytes.Clone(args.VMNeedV1),
	}
	defer func() {
		if failure != nil {
			failure = errors.Join(failure, witness.releaseImageLease())
		}
	}()

	switch args.Level {
	case "none":
		return nil, UserError("wall_unavailable", "sandbox preparation cannot test the none level", "Select process, gvisor or microvm when preparing a sandbox.")
	case "process":
		if args.Runtime != nil || len(args.VMNeedV1) != 0 {
			return nil, UserError("bad_request", "the process sandbox preparation takes no runtime or VM Need", "Prepare the process wall without runtime or VM Need fields.")
		}
		witness.driver = "process"
	case "gvisor":
		if args.Runtime == nil || len(args.VMNeedV1) != 0 {
			return nil, newRuntimeError("the gvisor sandbox preparation requires its selected runtime and no VM Need")
		}
		if _, err := gvisor.RuntimeItemsV1(*args.Runtime, runtime.GOARCH); err != nil {
			return nil, err
		}
		receipt, err := b.runtime.selectedRuntime(*args.Runtime)
		if err != nil {
			return nil, err
		}
		witness.runtime = receipt
		witness.driver = "gvisor"
	case "microvm":
		if len(args.VMNeedV1) == 0 {
			return nil, newRuntimeError("the microvm sandbox preparation requires an original client VM Need")
		}
		need, err := vmneed.DecodeV1(args.VMNeedV1, runtime.GOARCH)
		if err != nil {
			return nil, UserError("bad_request", "invalid sandbox prepare VM Need: "+err.Error(), "Prepare the microvm with the current client and release manifest.")
		}
		if need.GuestRAM != api.SandboxPrepareGuestRAMV1 || need.CPUs != api.SandboxPrepareCPUsV1 || need.DiskMax != api.SandboxPrepareDiskV1 {
			return nil, UserError("bad_request", "the microvm sandbox preparation dimensions differ from the fixed contract", "Use the fixed 512 MiB, 1 CPU and 1 GiB test-boot contract.")
		}
		if args.Runtime != nil && !reflect.DeepEqual(*args.Runtime, need.Runtime) {
			return nil, newRuntimeError("the sandbox prepare request carries two different runtime selections")
		}
		receipt, err := b.runtime.selectedRuntime(need.Runtime)
		if err != nil {
			return nil, err
		}
		defaultImage, err := receipt.defaultImageRequest(ctx, runtime.GOARCH)
		if err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(need.Image, defaultImage) {
			return nil, newRuntimeError("the sandbox prepare VM Need does not match this connection's release default image pin")
		}
		agentsReceipt, err := b.agents.selected(need.Agents)
		if err != nil {
			return nil, err
		}
		disk, err := agentsReceipt.Inventory()
		if err != nil {
			return nil, err
		}
		lease, imageSize, err := b.acquireCachedPrepareImageV1(ctx, need.Image)
		if err != nil {
			return nil, err
		}
		cloned := need.Clone()
		witness.runtime = receipt
		witness.agents = agentsReceipt
		witness.need = &cloned
		witness.driver = need.Driver
		witness.imageLease = lease
		witness.imageSize = imageSize
		witness.agentsSize = disk.Content.Size
	default:
		return nil, UserError("bad_request", "unsupported sandbox preparation level", "Use process, gvisor or microvm.")
	}
	witness.captured = true
	return witness, nil
}

func (b *bridge) acquireCachedPrepareImageV1(ctx context.Context, request image.Request) (*image.Lease, int64, error) {
	done, err := b.images.enter()
	if err != nil {
		return nil, 0, imageFailure(err)
	}
	defer done()
	if _, err := os.Lstat(paths.ImagesDir()); errors.Is(err, os.ErrNotExist) {
		return nil, 0, UserError("image_missing", "the release default image is not cached on this host", "Deliver or pull the default image before running sandbox preparation.")
	} else if err != nil {
		return nil, 0, imageFailure(err)
	}
	store, err := b.images.open(ctx)
	if err != nil {
		return nil, 0, imageFailure(err)
	}
	owned, finish, err := b.images.operationContext(ctx)
	if err != nil {
		return nil, 0, imageFailure(err)
	}
	defer finish()
	lease, err := store.Acquire(owned, request.PlatformDigest)
	if err != nil {
		return nil, 0, imageFailure(err)
	}
	cached, err := store.Cached(owned, request)
	if err != nil {
		_ = lease.Close()
		return nil, 0, imageFailure(err)
	}
	if !cached.Present || cached.Image == nil {
		_ = lease.Close()
		return nil, 0, UserError("image_missing", "the release default image is not cached on this host", "Deliver or pull the default image before running sandbox preparation.")
	}
	if err := image.CheckRecord(request, *cached.Image); err != nil {
		_ = lease.Close()
		return nil, 0, err
	}
	return lease, cached.Image.Size, nil
}

func bindAndSaveSandboxPrepareV1(operation *SessionOperation, reservation *sessionReservation, m *Manifest, witness *sandboxPrepareWitnessV1) error {
	if operation == nil || reservation == nil || m == nil || witness == nil ||
		!operation.Valid(m.ID) || reservation.id != m.ID || operation.native.borrowed {
		return newRuntimeError("sandbox prepare admission has no original reserved primary operation")
	}
	if err := validateSandboxPrepareBootManifestV1(m); err != nil {
		return err
	}
	witness.mu.Lock()
	defer witness.mu.Unlock()
	if !witness.captured || witness.bound != nil || witness.written || witness.bridge == nil || witness.bridge.ctx == nil || witness.bridge.ctx.Err() != nil {
		return newRuntimeError("the sandbox prepare witness was already consumed or its connection ended")
	}
	if !bytes.Equal(witness.needBytes, m.VMNeedV1) {
		return newRuntimeError("the sandbox prepare VM Need changed before admission")
	}
	if err := operation.native.rootRef.CheckRoot(reservation.dir); err != nil {
		return err
	}
	parentCheck, err := cachefs.CaptureRoot(reservation.parent)
	if err != nil {
		return err
	}
	if witness.runtime != nil {
		witness.runtime.mu.RLock()
		defer witness.runtime.mu.RUnlock()
		if err := witness.runtime.checkLocked(operation.ownerContext); err != nil {
			return err
		}
		if m.RuntimeManifest == nil || !reflect.DeepEqual(witness.runtime.manifest, *m.RuntimeManifest) {
			return newRuntimeError("the sandbox prepare runtime changed before admission")
		}
	} else if m.RuntimeManifest != nil || len(m.VMNeedV1) != 0 {
		return newRuntimeError("a raw sandbox prepare manifest cannot issue runtime authority")
	}
	if witness.agents != nil {
		witness.agents.mu.RLock()
		defer witness.agents.mu.RUnlock()
	}

	err = withPlacement(operation.ownerContext, false, func(control *placementControl) error {
		if err := checkUserDrainLockedV1(operation.ownerContext, control); err != nil {
			return err
		}
		if err := applyPersistentLockdownLockedV1(operation.ownerContext, control, m); err != nil {
			return err
		}
		if err := witness.checkProducersLocked(control); err != nil {
			return err
		}
		if err := operation.native.check(operation.ownerContext); err != nil {
			return err
		}
		if err := admitNewMemoryLockedV1(operation.ownerContext, operation, m); err != nil {
			return err
		}
		return operation.saveNewManifest(m)
	}, false)
	if err != nil {
		return err
	}
	witness.bound = operation
	witness.reservation, witness.parentCheck = reservation, parentCheck
	operation.sandboxPrepare = witness
	witness.written = true
	return nil
}

func (witness *sandboxPrepareWitnessV1) checkProducersLocked(control *placementControl) error {
	if witness.bridge == nil || witness.bridge.ctx == nil || witness.bridge.ctx.Err() != nil {
		return newRuntimeError("the original authenticated connection ended before sandbox prepare admission")
	}
	if witness.runtime == nil {
		return nil
	}
	check := func(publication *runtimePublication) error {
		if publication == nil || publication.operation == nil || publication.owner == nil || publication.ctx.Err() != nil {
			return newRuntimeError("an original runtime publication ended before sandbox prepare admission")
		}
		if current, ok := control.Active[publication.operation.token]; !ok || current != publication.operation.record {
			return newRuntimeError("the original runtime publication lost its active native reservation")
		}
		return publication.owner.checkIdentity()
	}
	if witness.runtime.closed {
		return newRuntimeError("the selected runtime receipt closed before sandbox prepare admission")
	}
	if err := check(witness.runtime.publication); err != nil {
		return err
	}
	if witness.agents != nil {
		if witness.agents.closed || witness.agents.lease == nil || witness.agents.store == nil ||
			witness.agents.publication == nil || witness.agents.publication.owner != witness.runtime.publication.owner {
			return newRuntimeError("the installed agents receipt no longer shares the original host owner")
		}
		if err := check(witness.agents.publication); err != nil {
			return err
		}
		if err := witness.agents.disk.Validate(runtime.GOARCH); err != nil {
			return err
		}
	}
	return nil
}
