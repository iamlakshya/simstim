package backend

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/vmneed"
	"github.com/iamlakshya/simstim/internal/wall"
)

// Paths names the exact descriptor-backed files and private endpoints selected
// by the owned runner. It carries no byte or execution authority itself.
type Paths struct {
	VMM, Kernel, Tools, Image, Agents, Session string
	Config, API, Vsock                         string
}

// bootConfiguration has only the devices used by this driver. It cannot
// express a network device, metadata service, snapshot, balloon or host share.
type bootConfiguration struct {
	BootSource        bootSource     `json:"boot-source"`
	Drives            []drive        `json:"drives"`
	Machine           machineConfig  `json:"machine-config"`
	NetworkInterfaces []struct{}     `json:"network-interfaces"`
	Vsock             vsockConfig    `json:"vsock"`
	Enclave           *enclaveConfig `json:"enclave,omitempty"`
}

// Direct mode uses ordinary virtio drivers and no dedicated CPU cores.
// No caller can enable debug, hotplug, DMBs, metadata or network devices.
type enclaveConfig struct {
	Direct     bool `json:"direct_mode"`
	Overcommit bool `json:"cpu_overcommit"`
}

type bootSource struct {
	Kernel string `json:"kernel_image_path"`
	Args   string `json:"boot_args"`
}

type drive struct {
	ID       string `json:"drive_id"`
	Path     string `json:"path_on_host"`
	Root     bool   `json:"is_root_device"`
	ReadOnly bool   `json:"is_read_only"`
	Cache    string `json:"cache_type"`
	IO       string `json:"io_engine"`
}

type machineConfig struct {
	CPUs       int   `json:"vcpu_count"`
	MemoryMiB  int64 `json:"mem_size_mib"`
	SMT        bool  `json:"smt"`
	DirtyPages bool  `json:"track_dirty_pages"`
}

type vsockConfig struct {
	CID  uint32 `json:"guest_cid"`
	Path string `json:"uds_path"`
}

// Command consumes the canonical admitted spec. The original runtime
// provenance, descriptor ownership and actual jail placement are established
// by the driver before this byte-only configuration step.
func Command(spec wall.Spec, paths Paths) ([]string, []byte, vmneed.MemoryBudget, error) {
	return command(spec, paths, "firecracker")
}

// NitroCommand uses the fixed direct-mode enclave backend and PCI transport.
// The same original admitted Need and retained descriptors remain required.
func NitroCommand(spec wall.Spec, paths Paths) ([]string, []byte, vmneed.MemoryBudget, error) {
	if spec.Driver != "nitro" {
		return nil, nil, vmneed.MemoryBudget{}, errors.New("the Nitro wall needs its explicit original driver plan")
	}
	return command(spec, paths, "nitro")
}

func command(spec wall.Spec, paths Paths, driver string) ([]string, []byte, vmneed.MemoryBudget, error) {
	if spec.Driver != "" && spec.Driver != driver {
		return nil, nil, vmneed.MemoryBudget{}, errors.New("the Firecracker transport conflicts with the original driver plan")
	}
	if spec.VM == nil {
		return nil, nil, vmneed.MemoryBudget{}, errors.New("the Firecracker wall needs its admitted VM plan")
	}
	need := vmneed.NeedV1{BootV1: spec.VM.Clone(), Driver: driver, CPUs: spec.CPUs, DiskMax: spec.DiskMax}
	if err := need.ValidateWall(need.Image.Arch, "microvm", driver, spec.MemoryMax); err != nil {
		return nil, nil, vmneed.MemoryBudget{}, err
	}
	if !api.ValidSessionID(spec.ID) {
		return nil, nil, vmneed.MemoryBudget{}, errors.New("the Firecracker wall needs a canonical session ID")
	}
	if len(spec.Mounts) != 0 {
		return nil, nil, vmneed.MemoryBudget{}, errors.New("the Firecracker wall cannot attach host folders")
	}
	if err := paths.validate(); err != nil {
		return nil, nil, vmneed.MemoryBudget{}, err
	}
	budget, err := vmneed.BudgetFor(need.GuestRAM)
	if err != nil {
		return nil, nil, vmneed.MemoryBudget{}, err
	}
	tools, _ := need.Runtime.Find(supply.KindTools, "tools", need.Image.Arch)
	config := bootConfiguration{
		BootSource:        bootSource{Kernel: paths.Kernel, Args: vmneed.KernelCommandLine(tools, need.Image)},
		Machine:           machineConfig{CPUs: need.CPUs, MemoryMiB: budget.Guest / vmneed.Mebibyte},
		NetworkInterfaces: []struct{}{},
		Vsock:             vsockConfig{CID: 3, Path: paths.Vsock},
	}
	// Firecracker's BlockBuilder keeps non-root devices in insertion order.
	// All four are non-root because dm-verity supplies /dev/dm-0. Keeping this
	// order binds vda/vdb to the two tables in the common kernel command line.
	for _, item := range []struct {
		id, path string
		readOnly bool
	}{
		{"tools", paths.Tools, true},
		{"image", paths.Image, true},
		{"agents", paths.Agents, true},
		{"session", paths.Session, false},
	} {
		config.Drives = append(config.Drives, drive{ID: item.id, Path: item.path, ReadOnly: item.readOnly, Cache: "Writeback", IO: "Sync"})
	}
	if driver == "nitro" {
		config.Enclave = &enclaveConfig{Direct: true, Overcommit: true}
	}
	data, err := json.Marshal(config)
	if err != nil {
		return nil, nil, vmneed.MemoryBudget{}, fmt.Errorf("encode Firecracker boot configuration: %w", err)
	}
	// MMIO is the production default. PCI is deliberately absent from this
	// driver's arguments. Nitro has a separate, explicit transport boundary.
	args := []string{paths.VMM, "--config-file", paths.Config, "--api-sock", paths.API, "--id", spec.ID}
	if driver == "nitro" {
		args = append(args, "--enable-pci")
	}
	return args, append(data, '\n'), budget, nil
}

func (p Paths) validate() error {
	seen := make(map[string]bool)
	for _, name := range []string{p.VMM, p.Kernel, p.Tools, p.Image, p.Agents, p.Session, p.Config, p.API, p.Vsock} {
		if !path.IsAbs(name) || path.Clean(name) != name || strings.ContainsAny(name, "\x00\r\n") {
			return errors.New("a Firecracker resource needs an absolute clean path")
		}
		if seen[name] {
			return errors.New("Firecracker resources cannot alias one path")
		}
		seen[name] = true
	}
	if len(p.API) > 107 || len(p.Vsock) > 107 {
		return errors.New("a private VMM socket path exceeds the Unix address limit")
	}
	return nil
}
