package client

import (
	"context"
	"reflect"
)

func hostMachineSource(h *Host) *MachineSourceProvenance {
	if h == nil || h.Machine == nil {
		return nil
	}
	return h.Machine.Source
}

func sameExistingMachineSource(before, after *Host) bool {
	a, b := hostMachineSource(before), hostMachineSource(after)
	if a == nil && b == nil {
		return true
	}
	return before != nil && after != nil && a != nil && b != nil && SameMachine(before.Machine, after.Machine) && reflect.DeepEqual(a, b)
}

func checkHostMachineSourceChange(ctx context.Context, change HostChange) error {
	if change.Now == nil || sameExistingMachineSource(change.Was, change.Now) {
		return nil
	}
	if hostMachineSource(change.Now) == nil {
		return hostStateRemovalError("a generic update cannot clear an existing machine-source record")
	}
	return checkHostMachineSourceBirthPublication(ctx, change)
}

func uniquePreviousMachineSource(hosts []Host, name string) (*Host, error) {
	var found *Host
	for i := range hosts {
		if hosts[i].Name != name {
			continue
		}
		if found != nil {
			if hostMachineSource(found) != nil || hostMachineSource(&hosts[i]) != nil {
				return nil, hostStateRemovalError("a repeated saved name cannot supply machine-source provenance")
			}
			continue // Preserve legacy duplicate groups with no provenance domain.
		}
		found = &hosts[i]
	}
	return found, nil
}

// Bulk replacement has no creation-token consumer. A public copied Machine
// DTO may preserve exact original provenance, never introduce or retarget it.
func checkGenericHostMachineSources(before, after []Host) error {
	for i := range after {
		previous, err := uniquePreviousMachineSource(before, after[i].Name)
		if err != nil {
			return err
		}
		if !sameExistingMachineSource(previous, &after[i]) {
			return hostStateRemovalError("bulk host replacement cannot mint, clear or change machine-source provenance")
		}
	}
	return nil
}

func checkHostMachineSourceRows(ctx context.Context, before, after []Host) error {
	for i := range after {
		previous, err := uniquePreviousMachineSource(before, after[i].Name)
		if err != nil {
			return err
		}
		if err := checkHostMachineSourceChange(ctx, HostChange{Was: previous, Now: &after[i]}); err != nil {
			return err
		}
	}
	return nil
}

// Typed encoding cannot discard unknown/future source-domain bytes. The ONE
// P114 normaliser rejects those using its canonical decoder's opaque status.
// A raw document mutation instead retains exact source data or fails the
// original-row comparison above. This helper supplies no creation authority.
func checkTypedHostMachineSources(ctx context.Context, hosts []Host) error {
	for i := range hosts {
		if err := ctx.Err(); err != nil {
			return err
		}
		if source := hostMachineSource(&hosts[i]); source != nil {
			if _, err := NormaliseMachineSource(hosts[i].Machine.Provider, source); err != nil {
				return err
			}
		}
	}
	return nil
}
