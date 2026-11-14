//go:build linux

package microvm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/runtimecache"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/vmneed"
	"github.com/iamlakshya/simstim/internal/wall"
)

// Options joins the driver to the canonical host facts and admitted session
// unit. Facts must omit wall-driver recursion and must come from the host.
type Options struct {
	Facts    func(context.Context) (api.CapProbe, error)
	Identity func() (string, error)
	// Domain supplies only the canonical host's pinned storage owner. It never
	// selects runtime, image or installed-agent execution bytes.
	Domain   func(context.Context) (cachefs.Domain, error)
	Boundary func(context.Context, MemoryBudget) error
	// Reference publishes only the validated client plan into the canonical
	// trusted session manifest while image/runtime leases are still held.
	Reference func(context.Context, wall.Spec) error
	// Diagnostic records at most the final 64 KiB outside the VM-data bind.
	// The host supplies its canonical supervisor log sink.
	Diagnostic func(context.Context, wall.Spec, []byte) error
	Artefacts  string
	Images     string
	Evidence   string
	Mkfs       string
}

// Driver constructs Cloud Hypervisor walls. Registration describes available
// code; Probe separately requires verified runtime and successful boot evidence.
type Driver struct{ options Options }

// New binds real host dependencies without creating a VM or changing a host.
func New(options Options) (*Driver, error) {
	if options.Facts == nil {
		return nil, fmt.Errorf("the microvm driver needs canonical host capability facts")
	}
	if options.Reference == nil {
		return nil, fmt.Errorf("the microvm driver needs the canonical trusted-manifest publisher")
	}
	if options.Identity == nil {
		return nil, fmt.Errorf("the microvm driver needs canonical host boot and helper identity")
	}
	if options.Domain == nil {
		return nil, fmt.Errorf("the microvm driver needs the canonical host storage domain")
	}
	if options.Boundary == nil {
		options.Boundary = CurrentMemoryBoundary
	}
	if options.Artefacts == "" {
		options.Artefacts = paths.ArtefactsDir()
	}
	if options.Images == "" {
		options.Images = paths.ImagesDir()
	}
	if options.Evidence == "" {
		options.Evidence = filepath.Join(paths.StateDir(), "chv-boot")
	}
	for _, path := range []string{options.Artefacts, options.Images, options.Evidence} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return nil, fmt.Errorf("microvm host dependencies need absolute clean paths")
		}
	}
	return &Driver{options: options}, nil
}

// Name is the stable Cloud Hypervisor driver name.
func (d *Driver) Name() string { return "chv" }

// Level is the microvm wall level.
func (d *Driver) Level() wall.Level { return wall.MicroVM }

// Probe combines canonical facts with verified cached runtime bytes. It
// invents no namespace/KVM probe and changes no host support or setting.
func (d *Driver) Probe(ctx context.Context) wall.Probe {
	result := wall.Probe{Driver: d.Name(), Level: d.Level(), Facts: map[string]string{}}
	facts, err := d.options.Facts(ctx)
	if err != nil {
		result.Reason = err.Error()
		result.Fix = "Check this host with host doctor."
		return result
	}
	if facts.OS != "linux" || facts.Arch != "amd64" && facts.Arch != "arm64" {
		result.Reason = "Cloud Hypervisor needs Linux on amd64 or arm64"
		return result
	}
	if !api.SupportsMicroVMKernel(d.Name(), facts.Kernel) {
		result.Reason = "this host kernel is below the supported Cloud Hypervisor floor"
		result.Fix = "Use a supported Linux kernel from version 6.8."
		return result
	}
	if !facts.KVM.Usable {
		result.Reason = "the microvm wall cannot use /dev/kvm"
		result.Fix = "Give this account KVM access with the root step."
		return result
	}
	if !facts.UserNS {
		result.Reason = "the VMM needs an available namespace jail with an empty network namespace"
		result.Fix = "Use the canonical namespace launcher and host preparation."
		return result
	}
	if !facts.UserManager || !facts.CgroupV2 {
		result.Reason = "the microvm wall needs the systemd user manager and cgroup v2"
		return result
	}
	if !facts.Seccomp || facts.LandlockABI < 1 {
		result.Reason = "the VMM needs seccomp and Landlock confinement"
		return result
	}
	if err := noDumpPrerequisite(); err != nil {
		result.Reason = err.Error()
		result.Fix = "Use a host whose existing fs.suid_dumpable policy is zero."
		return result
	}
	if _, _, err := formatterBinds(d.options.Mkfs); err != nil {
		result.Reason = err.Error()
		result.Fix = "Install the host's trusted e2fsprogs formatter and its system libraries."
		return result
	}
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
		if err := validateBootRuntime(record.Manifest, facts.Arch); err != nil {
			continue
		}
		vmm, ok := record.Manifest.Find(supply.KindVMM, "cloud-hypervisor", facts.Arch)
		if !ok || supply.Below(vmm.Version, "54.0") {
			continue
		}
		valid := true
		for _, selector := range bootRoles {
			item, ok := record.Manifest.Find(selector.Kind, selector.Name, facts.Arch)
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
		result.CanPrepare = true
		result.Version = vmm.Version
		if err := d.checkBootEvidence(record.Manifest, facts); err != nil {
			result.Reason = err.Error()
			result.Fix = "Complete the controlled guest boot check for this runtime and host."
			result.Facts["runtime_verified"] = "true"
			continue
		}
		result.OK = true
		result.Version = vmm.Version
		result.Facts["runtime"] = record.Manifest.Runtime
		result.Facts["runtime_verified"] = "true"
		result.Facts["uid_mode"] = uidMode(facts)
		return result
	}
	if result.Reason == "" {
		result.Reason = "the cache has no complete verified Cloud Hypervisor boot runtime"
		result.Fix = "Install this client's runtime with host upgrade."
	}
	return result
}

// RuntimeCompatibility supplies LevelsFor only with an actually booted,
// descriptor-verified runtime. The manifest is not execution authority.
func (d *Driver) RuntimeCompatibility(manifest supply.Manifest) (api.RuntimeDriver, bool) {
	if len(manifest.OwnFindings()) != 0 {
		return api.RuntimeDriver{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	facts, err := d.options.Facts(ctx)
	if err != nil || d.checkBootEvidence(manifest, facts) != nil {
		return api.RuntimeDriver{}, false
	}
	domain, err := d.options.Domain(ctx)
	if err != nil {
		return api.RuntimeDriver{}, false
	}
	store, err := runtimecache.OpenHostReadOnly(ctx, d.options.Artefacts, domain)
	if err != nil {
		return api.RuntimeDriver{}, false
	}
	defer store.Close()
	for _, selector := range bootRoles {
		item, ok := manifest.Find(selector.Kind, selector.Name, facts.Arch)
		if !ok || selector.Kind == supply.KindVMM && supply.Below(item.Version, "54.0") {
			return api.RuntimeDriver{}, false
		}
		file, err := store.OpenItem(ctx, item)
		if err != nil {
			return api.RuntimeDriver{}, false
		}
		file.Close()
	}
	return api.RuntimeDriver{Name: d.Name(), Arch: facts.Arch, KernelMin: "6.8", Verified: true}, true
}

func uidMode(facts api.CapProbe) string {
	if facts.Subuid && facts.KVM.InGroupManager {
		return "subuid"
	}
	return "login"
}

// Prepare stores a new host-owned VM specification; it formats no existing
// disk and starts no process. The caller supplies a private session folder.
func (d *Driver) Prepare(ctx context.Context, spec wall.Spec) (wall.Sandbox, error) {
	if err := validateVMSpec(spec); err != nil {
		return nil, err
	}
	facts, err := d.options.Facts(ctx)
	if err != nil {
		return nil, err
	}
	return d.prepare(ctx, spec, true, uidMode(facts))
}

// Build selects the new or stopped layout without deriving any execution
// authority from it. A malformed existing layout refuses rather than being
// replaced. The separate freshly validated ExpectedSpec remains required.
func (d *Driver) Build(ctx context.Context, spec wall.Spec) (wall.Sandbox, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	expected, ok := wall.ExpectedSpec(ctx)
	if !ok || expected.ID != spec.ID || expected.SessionDir != spec.SessionDir {
		return nil, fmt.Errorf("building a microvm needs its validated trusted expected plan")
	}
	spec = expected
	if err := validateVMSpec(spec); err != nil {
		return nil, err
	}
	if _, err := readVMRecord(spec.SessionDir); errors.Is(err, os.ErrNotExist) {
		return d.Prepare(ctx, spec)
	} else if err != nil {
		return nil, err
	}
	return d.Open(ctx, spec.SessionDir)
}

// Open reconstructs the saved stopped sandbox. It never accepts a saved PID
// or attaches ownership to a process that this driver did not start.
func (d *Driver) Open(ctx context.Context, sessionDir string) (wall.Sandbox, error) {
	record, err := readVMRecord(sessionDir)
	if err != nil {
		return nil, err
	}
	spec, ok := wall.ExpectedSpec(ctx)
	if !ok {
		return nil, fmt.Errorf("opening a microvm needs a freshly validated trusted expected plan")
	}
	if record.SessionDir != sessionDir || spec.SessionDir != sessionDir || record.ID != spec.ID {
		return nil, fmt.Errorf("saved microvm specification names another session directory")
	}
	if record.DiskMax != spec.DiskMax {
		return nil, fmt.Errorf("opening a microvm cannot resize its existing session disk")
	}
	if err := validateVMSpec(spec); err != nil {
		return nil, err
	}
	facts, err := d.options.Facts(ctx)
	if err != nil {
		return nil, err
	}
	if uidMode(facts) != record.UIDMode {
		return nil, fmt.Errorf("the microvm's persistent disk identity mode changed; no existing disk was formatted")
	}
	return d.prepare(ctx, spec, false, record.UIDMode)
}

func validateVMSpec(spec wall.Spec) error {
	if spec.VM == nil || spec.VM.Version != 1 {
		return fmt.Errorf("the microvm start needs version1 client runtime authority")
	}
	if spec.Image != nil {
		return fmt.Errorf("a microvm start cannot carry two image authorities")
	}
	if !api.ValidSessionID(spec.ID) || !filepath.IsAbs(spec.SessionDir) || filepath.Clean(spec.SessionDir) != spec.SessionDir {
		return fmt.Errorf("the microvm needs a session ID and private absolute directory")
	}
	apiPath, vsockPath := vmSocketPaths(vmDataDir(spec))
	for _, path := range []string{apiPath, vsockPath} {
		if len(path) > 107 || strings.ContainsAny(path, ",\x00\r\n") {
			return fmt.Errorf("the VM private socket path is too long or contains VMM option characters")
		}
	}
	need := vmneed.NeedV1{BootV1: *spec.VM, Driver: "chv", CPUs: spec.CPUs, DiskMax: spec.DiskMax}
	if err := need.Validate(runtime.GOARCH); err != nil {
		return err
	}
	budget, err := BudgetFor(spec.VM.GuestRAM)
	if err != nil {
		return err
	}
	if spec.MemoryMax != budget.Limit {
		return fmt.Errorf("the microvm unit cap differs from its guest RAM plus the canonical reserve")
	}
	return nil
}
