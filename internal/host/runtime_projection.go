package host

import (
	"context"
	"encoding/json"
	"reflect"
	"runtime"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

func init() {
	if runtime.GOOS == "linux" && (runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64") {
		Features = append(Features, api.FeatureRuntimeOriginV1)
	}
}

// A projection use narrows ONE actual original producer on this service. It
// owns no descriptor, receipt, publication, replacement manifest or lifetime.
type runtimeProjectionUseV1 struct {
	service  *runtimeService
	original *selectedRuntimeReceiptV1
	key      string
	driver   string
	selected supply.Manifest
}

// Only actual decoded VM consumers call this adapter. The full carrier picks
// exactly one retained map entry; it is never searched as a set of candidates.
func (s *runtimeService) selectedVMRuntimeOriginV1(ctx context.Context, driver string, expected supply.Manifest, raw json.RawMessage) (*selectedRuntimeReceiptV1, *runtimeProjectionUseV1, error) {
	if s == nil || ctx == nil {
		return nil, nil, newRuntimeError("the VM projection has no original runtime service or request lifetime")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if len(raw) == 0 {
		original, err := s.selectedRuntime(ctx, expected)
		return original, nil, err // Preserve the old exact-manifest contract only.
	}
	carrier, err := api.DecodeRuntimeOriginV1(raw)
	if err != nil {
		return nil, nil, err
	}
	key, err := selectedRuntimeKey(carrier.Manifest)
	if err != nil {
		return nil, nil, err
	}
	s.mu.Lock()
	original, closed := s.selections[key], s.closed
	s.mu.Unlock()
	if original == nil || closed || original.issuer != s {
		return nil, nil, newRuntimeError("this connection has no live original full runtime producer for the VM projection")
	}
	original.mu.RLock()
	defer original.mu.RUnlock()
	if !reflect.DeepEqual(original.manifest, carrier.Manifest) {
		return nil, nil, newRuntimeError("the full runtime origin differs from its actual original producer")
	}
	selected, err := vmneed.SelectRuntimeForDriver(driver, original.manifest, runtime.GOARCH)
	if err != nil {
		return nil, nil, newRuntimeError("the original full runtime has no valid selected driver roles: " + err.Error())
	}
	if !reflect.DeepEqual(selected, expected) {
		return nil, nil, newRuntimeError("the VM runtime differs from the canonical selected roles of its original producer")
	}
	use := &runtimeProjectionUseV1{service: s, original: original, key: key, driver: driver, selected: cloneSelectedRuntime(selected)}
	if err := use.verifyLocked(ctx); err != nil {
		return nil, nil, err
	}
	return original, use, nil
}

// The caller holds the SAME original receipt's read lock through its writer.
func (use *runtimeProjectionUseV1) verifyLocked(ctx context.Context) error {
	if err := use.checkActiveLocked(ctx); err != nil {
		return err
	}
	key, err := selectedRuntimeKey(use.original.manifest)
	if err != nil || key != use.key {
		return newRuntimeError("the retained full runtime producer changed its original key")
	}
	if err := use.original.checkLocked(ctx); err != nil {
		return err
	}
	selected, err := vmneed.SelectRuntimeForDriver(use.driver, use.original.manifest, runtime.GOARCH)
	if err != nil || !reflect.DeepEqual(selected, use.selected) {
		return newRuntimeError("the original runtime projection changed its canonical selected roles")
	}
	return nil
}

// Caller holds original.mu.RLock. This bounded identity check is safe inside
// placement and at pre-Rename; all descriptor bytes were checked beforehand.
func (use *runtimeProjectionUseV1) checkActiveLocked(ctx context.Context) error {
	if use == nil || use.original == nil || use.service == nil || use.original.issuer != use.service || ctx == nil {
		return newRuntimeError("the original runtime projection use is absent")
	}
	if use.original.key != use.key {
		return newRuntimeError("the original runtime projection full key changed")
	}
	return use.original.checkOriginLockedV1(ctx)
}

func selectedRuntimeInventoryV1(ctx context.Context, original *selectedRuntimeReceiptV1, use *runtimeProjectionUseV1) (supply.Manifest, error) {
	if use == nil {
		return original.inventory(ctx)
	}
	if use.original != original || original == nil {
		return supply.Manifest{}, newRuntimeError("the VM projection lost its same original runtime producer")
	}
	original.mu.RLock()
	defer original.mu.RUnlock()
	if err := use.verifyLocked(ctx); err != nil {
		return supply.Manifest{}, err
	}
	return cloneSelectedRuntime(use.selected), nil
}

func checkSelectedRuntimeManifestLockedV1(ctx context.Context, original *selectedRuntimeReceiptV1, use *runtimeProjectionUseV1, manifest *supply.Manifest) error {
	if original == nil || manifest == nil || ctx == nil {
		return newRuntimeError("the protected runtime has no original producer selection")
	}
	selected := original.manifest
	if use != nil {
		if use.original != original {
			return newRuntimeError("the protected runtime projection changed its original producer")
		}
		if err := use.verifyLocked(ctx); err != nil {
			return err
		}
		selected = use.selected
	} else if err := original.checkLocked(ctx); err != nil {
		return err
	}
	if !reflect.DeepEqual(selected, *manifest) {
		return newRuntimeError("the protected runtime differs from its original selected producer")
	}
	return nil
}

func checkSelectedRuntimeActiveLockedV1(ctx context.Context, original *selectedRuntimeReceiptV1, use *runtimeProjectionUseV1, manifest *supply.Manifest, driver string) error {
	if original == nil || manifest == nil || ctx == nil {
		return newRuntimeError("the final runtime writer has no original selection")
	}
	if err := original.checkOriginLockedV1(ctx); err != nil {
		return err
	}
	selected := original.manifest
	if use != nil {
		if use.original != original || use.service != original.issuer || use.driver != driver {
			return newRuntimeError("the final VM writer changed its original selected driver or producer")
		}
		if err := use.checkActiveLocked(ctx); err != nil {
			return err
		}
		selected = use.selected
	}
	if !reflect.DeepEqual(selected, *manifest) {
		return newRuntimeError("the final writer changed the original selected runtime")
	}
	return nil
}
