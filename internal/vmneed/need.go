package vmneed

import (
	"fmt"
	"slices"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/supply"
)

// BootV1 is the one version1 boot authority type. Values must originate from
// authenticated client Need or the protected trusted manifest recording it.
// Validation and byte-only ContentRef never create that provenance.
type BootV1 struct {
	Version  int               `json:"version"`
	GuestRAM int64             `json:"guest_ram"`
	Runtime  supply.Manifest   `json:"runtime"`
	Image    image.Request     `json:"image"`
	Agents   supply.ContentRef `json:"agents"`
}

// NeedV1 adds the driver and normalised limits to BootV1. Its JSON has exactly
// eight fields and contains no path, process identifier or descriptor.
type NeedV1 struct {
	BootV1
	Driver  string `json:"driver"`
	CPUs    int    `json:"cpus"`
	DiskMax int64  `json:"disk_max"`
}

// Clone copies the mutable vectors without changing any trusted metadata.
func (b BootV1) Clone() BootV1 {
	b.Runtime.Items = slices.Clone(b.Runtime.Items)
	b.Runtime.Advisories = slices.Clone(b.Runtime.Advisories)
	return b
}

// Clone returns an independent typed Need for publication or a context.
func (n NeedV1) Clone() NeedV1 { n.BootV1 = n.BootV1.Clone(); return n }

// Validate checks the complete known Need before persistence or effects. The
// authenticated owner separately applies grants, normalisation and admission.
func (n NeedV1) Validate(arch string) error {
	if err := n.BootV1.ValidateForDriver(n.Driver, arch); err != nil {
		return err
	}
	return ValidateLimitsForDriver(n.Driver, n.CPUs, n.DiskMax, arch)
}

// ValidateWall additionally rejects conflicting enclosing wall metadata and
// unit limits. Grants and actual unit placement remain with their sole owner.
func (n NeedV1) ValidateWall(arch, level, driver string, unitLimit int64) error {
	if err := n.Validate(arch); err != nil {
		return err
	}
	budget, err := BudgetFor(n.GuestRAM)
	if err != nil {
		return err
	}
	if level != "microvm" || driver != n.Driver || unitLimit != budget.Limit {
		return fmt.Errorf("the enclosing wall or unit cap conflicts with the trusted VM Need")
	}
	return nil
}

// Validate checks all boot metadata using existing canonical validators.
func (b BootV1) Validate(arch string) error {
	return b.ValidateForDriver("chv", arch)
}

// ValidateForDriver checks the same boot format for the selected driver. It
// never turns metadata into original-client or protected-operation authority.
func (b BootV1) ValidateForDriver(driver, arch string) error {
	if b.Version != 1 {
		return fmt.Errorf("the VM Need version is unsupported")
	}
	if arch != "amd64" && arch != "arm64" || b.Image.Arch != arch || b.Image.Transfer != "" {
		return fmt.Errorf("the VM Need architecture or image transfer is invalid")
	}
	if err := ValidateRuntimeForDriver(driver, b.Runtime, arch); err != nil {
		return err
	}
	if err := b.Image.Validate(); err != nil {
		return err
	}
	if b.Image.RootHash == "" || b.Image.DataBlocks <= 0 {
		return fmt.Errorf("the VM Need requires the original client image root hash and data block count")
	}
	if err := image.ValidateAgentsContentV1(b.Agents); err != nil {
		return err
	}
	_, err := BudgetFor(b.GuestRAM)
	return err
}

// ValidateLimits rejects values the pinned v54.1 KVM CLI cannot represent.
// Its config.rs MAX_SUPPORTED_CPUS is 8192 on x86_64 KVM and 255 on arm64.
// Host capacity and configuration defaults remain the admission owner's.
func ValidateLimits(cpus int, diskMax int64, arch string) error {
	return ValidateLimitsForDriver("chv", cpus, diskMax, arch)
}

// ValidateLimitsForDriver rejects values the pinned driver's KVM path cannot
// represent. It does not clamp guest RAM, CPUs or the admitted disk size.
func ValidateLimitsForDriver(driver string, cpus int, diskMax int64, arch string) error {
	if arch != "amd64" && arch != "arm64" {
		return fmt.Errorf("the VM limits name an unsupported architecture")
	}
	maximum := 255
	switch driver {
	case "chv":
		if arch == "amd64" {
			maximum = 8192
		}
	case "firecracker":
		// v1.18.0's KVM startup path allows 32 vCPUs on both architectures.
		// Odd counts are valid because this driver always disables SMT.
		maximum = 32
	default:
		return fmt.Errorf("the VM Need names an unsupported driver")
	}
	if cpus <= 0 || cpus > maximum || diskMax <= 0 {
		return fmt.Errorf("the VM Need has unrepresentable CPU or non-positive disk limits")
	}
	return nil
}
