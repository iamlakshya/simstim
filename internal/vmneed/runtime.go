package vmneed

import (
	"fmt"

	"github.com/iamlakshya/simstim/internal/runtimecache"
	"github.com/iamlakshya/simstim/internal/supply"
)

// A full runtime subset may include other drivers' runtime files. Only these
// exact roles can mint this driver's boot capabilities, once per role.
// Role selects one exact canonical runtime role.
type Role struct {
	Kind string
	Name string
}

// Roles returns a copy of the three closed boot selectors.
func Roles() []Role { return append([]Role(nil), bootRoles...) }

var bootRoles = []Role{{supply.KindVMM, "cloud-hypervisor"}, {supply.KindKernel, "guest"}, {supply.KindTools, "tools"}}

// ValidateRuntime checks exact boot roles through the canonical runtime validator.
func ValidateRuntime(manifest supply.Manifest, arch string) error {
	if err := runtimecache.Validate(manifest, arch); err != nil {
		return err
	}
	if findings := manifest.OwnFindings(); len(findings) != 0 {
		return fmt.Errorf("the client runtime matches an advisory: %s", findings[0].String())
	}
	for _, role := range bootRoles {
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
			if role.Kind == supply.KindVMM && supply.Below(item.Version, "54.0") {
				return fmt.Errorf("the VMM lacks required main-thread confinement")
			}
		}
		if count != 1 {
			return fmt.Errorf("the microvm runtime needs exactly one %s role", role.Kind)
		}
	}
	return nil
}
