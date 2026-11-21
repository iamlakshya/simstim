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
	"github.com/iamlakshya/simstim/internal/nitrodev"
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
	// ForkDiskV1 is installed by the closed host constructor. It borrows only
	// the genuine original new Boot/SAME OP's staged disk DATA, checking that
	// actual issuer before/after the callback. Presence grants no allocation,
	// execution or skip-format right; root still creates its own fresh target.
	ForkDiskV1 func(context.Context, func(*os.File) error) (bool, error)
	// ReservationV2 borrows the genuine accepted Cold/new-Boot's old reserved
	// native operands. A Spec, old journal or vector alone cannot supply it.
	ReservationV2 func(context.Context, func([]*os.File) error) (bool, error)
	// ReservationSelectedV2 is narrowing selection DATA from the SAME actual
	// accepted Boot receipt. It cannot supply operands or grant allocation.
	ReservationSelectedV2 func(context.Context) (bool, error)
	// Diagnostic records at most the final 64 KiB outside the VM-data bind.
	// The host supplies its canonical supervisor log sink.
	Diagnostic func(context.Context, wall.Spec, []byte) error
	Artefacts  string
	Images     string
	Evidence   string
	Mkfs       string
}

// Driver constructs one selected microvm backend. Registration describes available
// code; Probe separately requires verified runtime and successful boot evidence.
type Driver struct {
	options Options
	kind    backendKind
}

// New binds real host dependencies without creating a VM or changing a host.
func New(options Options) (*Driver, error) {
	return newDriver(options, cloudHypervisorBackend)
}

// NewForDriver selects one compiled backend. It cannot choose runtime bytes,
// grant admission or substitute host inventory for the original client plan.
func NewForDriver(name string, options Options) (*Driver, error) {
	switch name {
	case "chv":
		return newDriver(options, cloudHypervisorBackend)
	case "firecracker":
		return newDriver(options, firecrackerBackend)
	case "nitro":
		return newDriver(options, nitroBackend)
	default:
		return nil, fmt.Errorf("the microvm backend %q is unknown", name)
	}
}

func newDriver(options Options, kind backendKind) (*Driver, error) {
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
		options.Evidence = filepath.Join(paths.StateDir(), kind.name()+"-boot")
	}
	for _, path := range []string{options.Artefacts, options.Images, options.Evidence} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return nil, fmt.Errorf("microvm host dependencies need absolute clean paths")
		}
	}
	return &Driver{options: options, kind: kind}, nil
}

// Name is the stable selected backend name.
func (d *Driver) Name() string { return d.kind.name() }

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
		result.Reason = d.kind.label() + " needs Linux on amd64 or arm64"
		return result
	}
	if !api.SupportsMicroVMKernel(d.Name(), facts.Kernel) {
		result.Reason = "this host kernel is outside the supported " + d.kind.label() + " kernels"
		result.Fix = "Use a host kernel supported by this backend."
		return result
	}
	usable, accessBlocked := d.kind.deviceAccess(facts)
	if !usable && !accessBlocked {
		result.Reason = "the microvm wall cannot use " + d.kind.device()
		result.Fix = "Check the host's KVM device and virtualisation support, then probe again."
		if d.kind == nitroBackend {
			result.Fix = nitrodev.ParentKernelHint
		}
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
	return d.probeRuntimeV1(ctx, facts, result)
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
	if err != nil {
		return api.RuntimeDriver{}, false
	}
	// Keep the caller's full collection and its source/receipt untouched.
	selected, err := vmneed.SelectRuntimeForDriver(d.Name(), manifest, facts.Arch)
	if err != nil || d.checkBootEvidence(selected, facts) != nil {
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
	for _, selector := range d.kind.roles() {
		item, ok := selected.Find(selector.Kind, selector.Name, facts.Arch)
		if !ok || selector.Kind == supply.KindVMM && supply.Below(item.Version, d.kind.minimumVersion()) {
			return api.RuntimeDriver{}, false
		}
		file, err := store.OpenItem(ctx, item)
		if err != nil {
			return api.RuntimeDriver{}, false
		}
		file.Close()
	}
	result := api.RuntimeDriver{Name: d.Name(), Arch: facts.Arch, KernelMin: "6.8", Verified: true}
	if d.kind == firecrackerBackend {
		result.KernelMin = "5.10"
		result.KernelSeries = []string{"5.10", "6.1", "6.18"}
	}
	if d.kind == nitroBackend {
		result.KernelMin = ""
	}
	return result, true
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
	if err := d.validateSpec(spec); err != nil {
		return nil, err
	}
	facts, err := d.options.Facts(ctx)
	if err != nil {
		return nil, err
	}
	mode, err := slotPreparationModeV1(ctx, facts)
	if d.kind == nitroBackend && mode != slotUIDModeV1Name {
		mode = "login"
	}
	if err != nil {
		return nil, err
	}
	if selected, err := d.reservationSelectionV2(ctx); err != nil {
		return nil, err
	} else if selected {
		if !slotAccountModeV1(facts) {
			return nil, fmt.Errorf("the actual reserved Boot requires its existing exact slot creator")
		}
		mode = slotUIDModeV1Name
	}
	return d.prepare(ctx, spec, true, mode)
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
	if err := d.validateSpec(spec); err != nil {
		return nil, err
	}
	if _, err := readVMRecordFor(spec.SessionDir, d.kind); errors.Is(err, os.ErrNotExist) {
		return d.Prepare(ctx, spec)
	} else if err != nil {
		return nil, err
	}
	return d.Open(ctx, spec.SessionDir)
}

// Open reconstructs the saved stopped sandbox. It never accepts a saved PID
// or attaches ownership to a process that this driver did not start.
func (d *Driver) Open(ctx context.Context, sessionDir string) (wall.Sandbox, error) {
	record, err := readVMRecordFor(sessionDir, d.kind)
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
	if err := d.validateSpec(spec); err != nil {
		return nil, err
	}
	facts, err := d.options.Facts(ctx)
	if err != nil {
		return nil, err
	}
	if !(d.kind == nitroBackend && record.UIDMode == "login") && !savedUIDModeV1(facts, record.UIDMode) {
		return nil, fmt.Errorf("the microvm's persistent disk identity mode changed; no existing disk was formatted")
	}
	if selected, err := d.reservationSelectionV2(ctx); err != nil {
		return nil, err
	} else if selected && record.UIDMode != slotUIDModeV1Name {
		return nil, fmt.Errorf("the actual reserved Boot cannot use an ordinary or subuid disk creator")
	}
	return d.prepare(ctx, spec, false, record.UIDMode)
}

func (d *Driver) reservationSelectionV2(ctx context.Context) (bool, error) {
	if d.options.ReservationSelectedV2 == nil {
		return false, nil
	}
	selected, err := d.options.ReservationSelectedV2(ctx)
	if err != nil {
		return false, err
	}
	if selected && d.options.ReservationV2 == nil {
		return false, fmt.Errorf("the actual reserved Boot selector has no genuine original input provider")
	}
	return selected, nil
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
	kind, err := backendForSpec(spec)
	if err != nil {
		return err
	}
	if len(spec.Mounts) != 0 {
		return fmt.Errorf("a microvm cannot attach host folders")
	}
	need := vmneed.NeedV1{BootV1: *spec.VM, Driver: kind.name(), CPUs: spec.CPUs, DiskMax: spec.DiskMax}
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

func (d *Driver) validateSpec(spec wall.Spec) error {
	if err := validateVMSpec(spec); err != nil {
		return err
	}
	kind, err := backendForSpec(spec)
	if err != nil || kind != d.kind {
		return fmt.Errorf("the selected VMM differs from this driver's original boot plan")
	}
	return nil
}
