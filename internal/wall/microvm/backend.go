package microvm

import (
	"fmt"

	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/vmneed"
	"github.com/iamlakshya/simstim/internal/wall"
)

type backendKind uint8

const (
	cloudHypervisorBackend backendKind = iota
	firecrackerBackend
)

func (k backendKind) name() string {
	if k == firecrackerBackend {
		return "firecracker"
	}
	return "chv"
}

func (k backendKind) label() string {
	if k == firecrackerBackend {
		return "Firecracker"
	}
	return "Cloud Hypervisor"
}

func (k backendKind) vmmName() string {
	if k == firecrackerBackend {
		return "firecracker"
	}
	return "cloud-hypervisor"
}

func (k backendKind) minimumVersion() string {
	if k == firecrackerBackend {
		return "1.17.0"
	}
	return "54.0"
}

func (k backendKind) roles() []vmneed.Role {
	// The kind is private and constructors admit only these two constants.
	return []vmneed.Role{{Kind: supply.KindVMM, Name: k.vmmName()}, {Kind: supply.KindKernel, Name: "guest"}, {Kind: supply.KindTools, Name: "tools"}}
}

func (k backendKind) validateRuntime(manifest supply.Manifest, arch string) error {
	return vmneed.ValidateRuntimeForDriver(k.name(), manifest, arch)
}

// backendForSpec selects a closed implementation from the already supplied
// plan. The caller still requires original operation/byte/lifetime authority.
// This helper does not turn a manifest or a saved layout into a capability.
func backendForSpec(spec wall.Spec) (backendKind, error) {
	if spec.VM == nil {
		return 0, fmt.Errorf("the microvm needs its original boot plan")
	}
	var selected backendKind
	count := 0
	for _, item := range spec.VM.Runtime.Items {
		if item.Kind != supply.KindVMM {
			continue
		}
		count++
		switch item.Name {
		case "cloud-hypervisor":
			selected = cloudHypervisorBackend
		case "firecracker":
			selected = firecrackerBackend
		default:
			return 0, fmt.Errorf("the microvm plan names an unknown VMM")
		}
	}
	if count != 1 {
		return 0, fmt.Errorf("the microvm plan needs exactly one selected VMM")
	}
	return selected, nil
}
