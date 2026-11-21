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
	nitroBackend
)

func (k backendKind) name() string {
	if k == nitroBackend {
		return "nitro"
	}
	if k == firecrackerBackend {
		return "firecracker"
	}
	return "chv"
}

func (k backendKind) label() string {
	if k == nitroBackend {
		return "Nitro Sandboxes"
	}
	if k.isFirecracker() {
		return "Firecracker"
	}
	return "Cloud Hypervisor"
}

func (k backendKind) vmmName() string {
	if k.isFirecracker() {
		return "firecracker"
	}
	return "cloud-hypervisor"
}

func (k backendKind) minimumVersion() string {
	if k == nitroBackend {
		return "1.18.0"
	}
	if k == firecrackerBackend {
		return "1.17.0"
	}
	return "54.0"
}

func (k backendKind) isFirecracker() bool { return k == firecrackerBackend || k == nitroBackend }

func (k backendKind) device() string {
	if k == nitroBackend {
		return "/dev/nitro_enclaves"
	}
	return "/dev/kvm"
}

func (k backendKind) kernelName() string {
	if k == nitroBackend {
		return "guest-efi"
	}
	return "guest"
}

func (k backendKind) roles() []vmneed.Role {
	roles, _ := vmneed.RolesForDriver(k.name())
	return roles
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
	if spec.Driver == "nitro" && selected == firecrackerBackend {
		return nitroBackend, nil
	}
	if spec.Driver != "" && spec.Driver != selected.name() {
		return 0, fmt.Errorf("the selected driver conflicts with the original VMM plan")
	}
	return selected, nil
}
