package vmneed

import (
	"fmt"

	"github.com/iamlakshya/simstim/internal/runtimecache"
	"github.com/iamlakshya/simstim/internal/supply"
)

// Role selects one exact canonical runtime role.
type Role struct {
	Kind string
	Name string
}

// Roles returns the three closed Cloud Hypervisor boot selectors.
func Roles() []Role {
	roles, _ := RolesForDriver("chv")
	return roles
}

// RolesForDriver returns a fresh vector of exact boot roles. A selected Need
// contains one VMM, not a host-selected alternative from an inventory.
func RolesForDriver(driver string) ([]Role, error) {
	name := ""
	switch driver {
	case "chv":
		name = "cloud-hypervisor"
	case "firecracker", "nitro":
		name = "firecracker"
	default:
		return nil, fmt.Errorf("the VM Need names an unsupported driver")
	}
	kernel := "guest"
	if driver == "nitro" {
		kernel = "guest-efi"
	}
	return []Role{{supply.KindVMM, name}, {supply.KindKernel, kernel}, {supply.KindTools, "tools"}}, nil
}

// ValidateRuntime checks exact boot roles through the canonical runtime validator.
func ValidateRuntime(manifest supply.Manifest, arch string) error {
	return ValidateRuntimeForDriver("chv", manifest, arch)
}

// ValidateRuntimeForDriver checks the selected driver's closed role set with
// the canonical manifest, architecture and advisory validators. It validates
// bytes and metadata only. The original owner separately supplies authority.
func ValidateRuntimeForDriver(driver string, manifest supply.Manifest, arch string) error {
	roles, err := RolesForDriver(driver)
	if err != nil {
		return err
	}
	if err := runtimecache.Validate(manifest, arch); err != nil {
		return err
	}
	if findings := manifest.OwnFindings(); len(findings) != 0 {
		return fmt.Errorf("the client runtime matches an advisory: %s", findings[0].String())
	}
	for _, role := range roles {
		count := 0
		for _, item := range manifest.Items {
			if item.Kind != role.Kind {
				continue
			}
			count++
			if item.Name != role.Name {
				return fmt.Errorf("the microvm runtime names an unexpected %s role", role.Kind)
			}
			if role.Kind == supply.KindTools {
				if err := supply.CheckToolsMetadata(item); err != nil {
					return err
				}
			}
			if role.Kind == supply.KindVMM {
				minimum := "54.0"
				if driver == "firecracker" {
					minimum = "1.17.0"
				}
				if driver == "nitro" {
					minimum = "1.18.0"
				}
				if supply.Below(item.Version, minimum) {
					if driver == "chv" {
						return fmt.Errorf("the VMM lacks required main-thread confinement")
					}
					return fmt.Errorf("the %s runtime is below the required version %s", role.Name, minimum)
				}
			}
		}
		if count != 1 {
			return fmt.Errorf("the microvm runtime needs exactly one %s role", role.Kind)
		}
	}
	return nil
}

// SelectRuntimeForDriver derives one driver's exact boot metadata from the
// original architecture-specific runtime collection. The caller retains the
// complete verified signed release, delivery request and native receipt.
// This output is not a newly signed manifest, store record or execution source.
func SelectRuntimeForDriver(driver string, original supply.Manifest, arch string) (supply.Manifest, error) {
	if err := runtimecache.Validate(original, arch); err != nil {
		return supply.Manifest{}, err
	}
	roles, err := RolesForDriver(driver)
	if err != nil {
		return supply.Manifest{}, err
	}
	selected := supply.Manifest{
		Release:    original.Release,
		Runtime:    original.Runtime,
		Advisories: append([]supply.Advisory(nil), original.Advisories...),
	}
	for _, role := range roles {
		item, found := original.Find(role.Kind, role.Name, arch)
		if !found {
			return supply.Manifest{}, fmt.Errorf("the original runtime lacks the selected %s role %s", role.Kind, role.Name)
		}
		selected.Items = append(selected.Items, item)
	}
	if err := ValidateRuntimeForDriver(driver, selected, arch); err != nil {
		return supply.Manifest{}, err
	}
	return selected, nil
}
