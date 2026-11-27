//go:build linux

package microvm

import (
	"context"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/runtimecache"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/vmneed"
	"github.com/iamlakshya/simstim/internal/wall"
)

// probeRuntimeV1 follows the physical prerequisite checks in Probe. Runtime
// inventory is descriptive: boot and admission retain their original producers.
func (d *Driver) probeRuntimeV1(ctx context.Context, facts api.CapProbe, result wall.Probe) wall.Probe {
	domain, err := d.options.Domain(ctx)
	if err != nil {
		result.Reason = err.Error()
		return result
	}
	store, err := runtimecache.OpenHostReadOnly(ctx, d.options.Artefacts, domain)
	if err != nil {
		result.Reason = "the verified microvm runtime is missing"
		result.Fix = "Install this client's runtime with host upgrade."
		return result
	}
	defer store.Close()
	records, err := store.RecordsContext(ctx, facts.Arch)
	if err != nil {
		result.Reason = err.Error()
		return result
	}
	for _, record := range records {
		// The stored collection remains intact. This is comparison metadata
		// for one driver, never a replacement signed record or boot grant.
		selected, err := vmneed.SelectRuntimeForDriver(d.Name(), record.Manifest, facts.Arch)
		if err != nil {
			continue
		}
		if err := d.kind.validateRuntime(selected, facts.Arch); err != nil {
			continue
		}
		vmm, ok := selected.Find(supply.KindVMM, d.kind.vmmName(), facts.Arch)
		if !ok || supply.Below(vmm.Version, d.kind.minimumVersion()) {
			continue
		}
		valid := true
		for _, selector := range d.kind.roles() {
			item, ok := selected.Find(selector.Kind, selector.Name, facts.Arch)
			if !ok {
				valid = false
				break
			}
			file, err := store.OpenItem(ctx, item)
			if err != nil {
				valid = false
				break
			}
			file.Close()
		}
		if !valid {
			continue
		}
		if _, accessBlocked := d.kind.deviceAccess(facts); accessBlocked {
			result.Version = vmm.Version
			result.Reason = "the microvm wall cannot open " + d.kind.device() + " with this account"
			result.Fix = "Give this account device access with the root step, then probe again."
			result.Facts["root_blocker"] = "kvm_access"
			if d.kind == nitroBackend {
				result.Facts["root_blocker"] = "nitro_access"
			}
			result.Facts["runtime_verified"] = "true"
			return result
		}
		result.CanPrepare = true
		result.Version = vmm.Version
		if err := d.checkBootEvidence(selected, facts); err != nil {
			result.Reason = err.Error()
			result.Fix = "Complete the controlled guest boot check for this runtime and host."
			result.Facts["runtime_verified"] = "true"
			continue
		}
		result.OK = true
		result.Version = vmm.Version
		result.Facts["runtime"] = selected.Runtime
		result.Facts["runtime_verified"] = "true"
		result.Facts["uid_mode"] = d.kind.identityMode(facts)
		return result
	}
	if result.Reason == "" {
		result.Reason = "the cache has no complete verified " + d.kind.label() + " boot runtime"
		result.Fix = "Install this client's runtime with host upgrade."
	}
	return result
}
