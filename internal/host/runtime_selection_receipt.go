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

	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/runtimecache"
	"github.com/iamlakshya/simstim/internal/supply"
)

type heldRuntimeItemV1 struct {
	item supply.Item
	file *os.File
}

// selectedRuntimeReceiptV1 is issued only by this bridge's actual successful
// runtime delivery/status request. All selected original role descriptors stay
// held through the protected request writer and connection lifetime.
type selectedRuntimeReceiptV1 struct {
	mu          sync.RWMutex
	fileMu      sync.Mutex
	once        sync.Once
	manifest    supply.Manifest
	publication *runtimePublication
	store       *runtimecache.Store
	files       []heldRuntimeItemV1
	closed      bool
	closeErr    error
}

func cloneSelectedRuntime(manifest supply.Manifest) supply.Manifest {
	manifest.Items = append([]supply.Item(nil), manifest.Items...)
	manifest.Advisories = append([]supply.Advisory(nil), manifest.Advisories...)
	return manifest
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
func (s *runtimeService) rememberRuntime(ctx context.Context, manifest supply.Manifest) (failure error) {
	if err := runtimecache.Validate(manifest, runtime.GOARCH); err != nil {
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
		_, err := existing.inventory(ctx)
		return err
	}
	publication, err := beginRuntimePublication(ctx)
	if err != nil {
		return err
	}
	receipt := &selectedRuntimeReceiptV1{manifest: cloneSelectedRuntime(manifest), publication: publication}
	retained := false
	defer func() {
		if !retained {
			failure = errors.Join(failure, receipt.Close())
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
	defer s.mu.Unlock()
	if s.closed {
		return newRuntimeError("the authenticated runtime connection ended before selection publication")
	}
	if s.selections == nil {
		s.selections = map[string]*selectedRuntimeReceiptV1{}
	}
	if s.selections[key] != nil {
		return nil
	}
	s.selections[key], retained = receipt, true
	return nil
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
		for _, held := range receipt.files {
			receipt.closeErr = errors.Join(receipt.closeErr, held.file.Close())
		}
		if receipt.store != nil {
			receipt.closeErr = errors.Join(receipt.closeErr, receipt.store.Close())
		}
		if receipt.publication != nil {
			receipt.closeErr = errors.Join(receipt.closeErr, receipt.publication.close())
		}
	})
	return receipt.closeErr
}
