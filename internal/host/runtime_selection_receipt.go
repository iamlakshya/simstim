package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"runtime"
	"sync"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/runtimecache"
	"github.com/iamlakshya/simstim/internal/supply"
)

type heldRuntimeItemV1 struct {
	item     supply.Item
	file     *os.File
	released bool
}

// selectedRuntimeReceiptV1 is issued only by this bridge's actual successful
// runtime delivery/status request. All selected original role descriptors stay
// held through the protected request writer and connection lifetime.
type selectedRuntimeReceiptV1 struct {
	closeMu       sync.Mutex
	mu            sync.RWMutex
	fileMu        sync.Mutex
	once          sync.Once
	manifest      supply.Manifest
	imageDefaults *[]supply.Item
	publication   *runtimePublication
	store         *runtimecache.Store
	files         []heldRuntimeItemV1
	closed        bool
	closeErr      error
}

func cloneSelectedRuntime(manifest supply.Manifest) supply.Manifest {
	manifest.Items = append([]supply.Item(nil), manifest.Items...)
	manifest.Advisories = append([]supply.Advisory(nil), manifest.Advisories...)
	return manifest
}

func cloneImageDefaults(defaults *[]supply.Item) *[]supply.Item {
	if defaults == nil {
		return nil
	}
	cloned := append([]supply.Item(nil), (*defaults)...)
	return &cloned
}

func startupDigest(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func selectedRuntimeKey(manifest supply.Manifest) (string, error) {
	raw, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	return startupDigest(raw), nil
}

func (s *runtimeService) selectedRuntime(expected supply.Manifest) (*selectedRuntimeReceiptV1, error) {
	key, err := selectedRuntimeKey(expected)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	receipt, closed := s.selections[key], s.closed
	s.mu.Unlock()
	if closed || receipt == nil {
		return nil, newRuntimeError("this authenticated connection has no original held runtime selection")
	}
	actual, err := receipt.inventory(context.Background())
	if err != nil || !reflect.DeepEqual(actual, expected) {
		return nil, errors.Join(newRuntimeError("the new request differs from its same-connection runtime selection"), err)
	}
	return receipt, nil
}

// rememberRuntime executes only after the same original request has checked
// every named runtime object. A bare supplied manifest cannot call New's issuer.
func (s *runtimeService) rememberRuntime(ctx context.Context, manifest supply.Manifest) error {
	return s.rememberRuntimeWithDefaults(ctx, manifest, nil)
}

// rememberRuntimeWithDefaults retains same-connection request ImageDefaults
// solely to cross-check an original client's default-image pin on this bridge.
// On-disk Store.ImageDefaults is retention-only and never populates a receipt.
func (s *runtimeService) rememberRuntimeWithDefaults(ctx context.Context, manifest supply.Manifest, defaults *[]supply.Item) (failure error) {
	done, err := s.enterRuntimeV1()
	if err != nil {
		return err
	}
	defer done()
	unlock, err := s.lockRuntimeSelectionV1(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	if err := s.retryRuntimeSelectionV1(); err != nil {
		return err
	}
	if err := runtimecache.ValidateWithImageDefaults(manifest, defaults, runtime.GOARCH); err != nil {
		return err
	}
	if findings := manifest.OwnFindings(); len(findings) != 0 {
		return newRuntimeError("the original selected runtime matches a known advisory")
	}
	key, err := selectedRuntimeKey(manifest)
	if err != nil {
		return err
	}
	s.mu.Lock()
	closed, existing := s.closed, s.selections[key]
	s.mu.Unlock()
	if closed {
		return newRuntimeError("the authenticated runtime connection has ended")
	}
	if existing != nil {
		return existing.retainImageDefaults(ctx, defaults)
	}
	publication, err := beginRuntimePublication(ctx)
	if err != nil {
		return err
	}
	receipt := &selectedRuntimeReceiptV1{
		manifest:      cloneSelectedRuntime(manifest),
		imageDefaults: cloneImageDefaults(defaults),
		publication:   publication,
	}
	retained := false
	defer func() {
		if !retained {
			cleanup := receipt.Close()
			failure = errors.Join(failure, cleanup)
			if cleanup != nil {
				s.mu.Lock()
				s.selectionPending = receipt
				s.mu.Unlock()
			}
		}
	}()
	receipt.store, err = runtimecache.OpenHostReadOnly(publication.ctx, paths.ArtefactsDir(), publication.owner.domain())
	if err != nil {
		return err
	}
	for _, item := range receipt.manifest.Items {
		file, err := receipt.store.OpenItem(publication.ctx, item)
		if err != nil {
			return err
		}
		receipt.files = append(receipt.files, heldRuntimeItemV1{item: item, file: file})
	}
	if err := receipt.checkLocked(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return newRuntimeError("the authenticated runtime connection ended before selection publication")
	}
	if s.selections == nil {
		s.selections = map[string]*selectedRuntimeReceiptV1{}
	}
	if current := s.selections[key]; current != nil {
		s.mu.Unlock()
		return current.retainImageDefaults(ctx, defaults)
	}
	s.selections[key], retained = receipt, true
	s.mu.Unlock()
	return nil
}

func (receipt *selectedRuntimeReceiptV1) retainImageDefaults(ctx context.Context, defaults *[]supply.Item) error {
	if receipt == nil {
		return newRuntimeError("the selected runtime receipt is missing")
	}
	receipt.mu.Lock()
	defer receipt.mu.Unlock()
	if err := receipt.checkLocked(ctx); err != nil {
		return err
	}
	if defaults == nil {
		return nil
	}
	if receipt.imageDefaults == nil {
		receipt.imageDefaults = cloneImageDefaults(defaults)
		return nil
	}
	if !reflect.DeepEqual(*receipt.imageDefaults, *defaults) {
		return newRuntimeError("the runtime request carries conflicting default-image pins on this connection")
	}
	return nil
}

// defaultImageRequest returns the exact default-image pin supplied on this
// same authenticated connection. It never falls back to Store.ImageDefaults.
func (receipt *selectedRuntimeReceiptV1) defaultImageRequest(ctx context.Context, arch string) (image.Request, error) {
	if receipt == nil {
		return image.Request{}, newRuntimeError("the selected runtime receipt is missing")
	}
	receipt.mu.RLock()
	defer receipt.mu.RUnlock()
	if err := receipt.checkLocked(ctx); err != nil {
		return image.Request{}, err
	}
	if receipt.imageDefaults == nil {
		return image.Request{}, newRuntimeError("this connection's runtime selection carries no original default-image pins")
	}
	manifest := supply.Manifest{Release: receipt.manifest.Release, Items: *receipt.imageDefaults}
	item, ok := manifest.Find(supply.KindImage, "agent-base", arch)
	if !ok {
		return image.Request{}, newRuntimeError("this connection's runtime selection has no default image pin for " + arch)
	}
	req := image.Request{
		Ref:            brand.ImageRepository + "@" + item.Digest,
		Digest:         item.Digest,
		PlatformDigest: item.Digest,
		Arch:           arch,
		RootHash:       item.RootHash,
		DataBlocks:     item.DataBlocks,
	}
	if err := req.Validate(); err != nil {
		return image.Request{}, err
	}
	if req.RootHash == "" || req.DataBlocks <= 0 {
		return image.Request{}, newRuntimeError("the default image pin is missing its verity root hash or block count")
	}
	return req, nil
}

func (receipt *selectedRuntimeReceiptV1) checkLocked(ctx context.Context) error {
	if receipt == nil || receipt.closed || receipt.publication == nil || receipt.store == nil ||
		len(receipt.files) != len(receipt.manifest.Items) || receipt.publication.ctx.Err() != nil {
		return newRuntimeError("the original held runtime producer ended")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := receipt.publication.owner.checkIdentity(); err != nil {
		return err
	}
	receipt.fileMu.Lock()
	defer receipt.fileMu.Unlock()
	for _, held := range receipt.files {
		if held.file == nil {
			return newRuntimeError("the original selected runtime lost a role descriptor")
		}
		if err := supply.CheckOpenContext(ctx, held.item, held.file); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (receipt *selectedRuntimeReceiptV1) inventory(ctx context.Context) (supply.Manifest, error) {
	if receipt == nil {
		return supply.Manifest{}, newRuntimeError("the selected runtime receipt is missing")
	}
	receipt.mu.RLock()
	defer receipt.mu.RUnlock()
	if err := receipt.checkLocked(ctx); err != nil {
		return supply.Manifest{}, err
	}
	return cloneSelectedRuntime(receipt.manifest), nil
}

func (receipt *selectedRuntimeReceiptV1) Close() error {
	if receipt == nil {
		return nil
	}
	receipt.once.Do(func() {
		receipt.mu.Lock()
		defer receipt.mu.Unlock()
		receipt.closed = true
	})
	receipt.closeMu.Lock()
	defer receipt.closeMu.Unlock()
	for index := range receipt.files {
		held := &receipt.files[index]
		if held.file == nil || held.released {
			continue
		}
		err := held.file.Close()
		held.released = true // This exact os.File is unusable after Close, including on error.
		if err != nil {
			receipt.closeErr = err
			return err
		}
	}
	if receipt.store != nil {
		if err := receipt.store.Close(); err != nil {
			receipt.closeErr = err
			return err
		}
		receipt.store = nil
	}
	if receipt.publication != nil {
		if err := receipt.publication.close(); err != nil {
			receipt.closeErr = err
			return err
		}
		receipt.publication = nil
	}
	receipt.closeErr = nil
	return nil
}
