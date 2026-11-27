//go:build linux

package host

import (
	"context"
	"errors"
	"fmt"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/wall"
	"github.com/iamlakshya/simstim/internal/wall/chv"
)

// The registry holds implementation availability, not readiness. Resolve the
// current host paths at each use, including in isolated helper processes.
var cloudHypervisorDriver cloudHypervisorRegistration

func init() {
	if err := wall.Register(&cloudHypervisorDriver); err != nil {
		panic(err)
	}
}

type cloudHypervisorRegistration struct{}

func (*cloudHypervisorRegistration) Name() string      { return "chv" }
func (*cloudHypervisorRegistration) Level() wall.Level { return wall.MicroVM }
func (r *cloudHypervisorRegistration) Probe(ctx context.Context) wall.Probe {
	driver, err := NewCloudHypervisorDriver()
	if err != nil {
		return wall.Probe{Driver: r.Name(), Level: r.Level(), Reason: err.Error(), Fix: "Check the host's private state and data folders."}
	}
	return driver.Probe(ctx)
}
func (*cloudHypervisorRegistration) Prepare(ctx context.Context, spec wall.Spec) (wall.Sandbox, error) {
	driver, err := NewCloudHypervisorDriver()
	if err != nil {
		return nil, err
	}
	return driver.Prepare(ctx, spec)
}
func (*cloudHypervisorRegistration) Open(ctx context.Context, dir string) (wall.Sandbox, error) {
	driver, err := NewCloudHypervisorDriver()
	if err != nil {
		return nil, err
	}
	return driver.Open(ctx, dir)
}

func (*cloudHypervisorRegistration) Build(ctx context.Context, spec wall.Spec) (wall.Sandbox, error) {
	driver, err := NewCloudHypervisorDriver()
	if err != nil {
		return nil, err
	}
	return driver.Build(ctx, spec)
}
func (*cloudHypervisorRegistration) RuntimeCompatibility(manifest supply.Manifest) (api.RuntimeDriver, bool) {
	driver, err := NewCloudHypervisorDriver()
	if err != nil {
		return api.RuntimeDriver{}, false
	}
	return driver.RuntimeCompatibility(manifest)
}

type vmResourcesV1 struct {
	driver     wall.Driver
	makeDriver func() (wall.Driver, error)
}

// CloudHypervisorResourcesV1 returns the actual registered driver controller.
// Only the trusted supervisor calls it with canonical trusted ExpectedSpec;
// neither a bridge request nor a guest operation can create that authority.
func CloudHypervisorResourcesV1() wall.ResourceFactoryV1 {
	return &vmResourcesV1{driver: &cloudHypervisorDriver}
}

// CloudHypervisorResourcesWithPublisherV1 binds the live supervisor's sole
// manifest publisher. Its callback saves the same live trusted record, so a
// later supervisor save cannot overwrite the driver's retention references.
func CloudHypervisorResourcesWithPublisherV1(publish func(context.Context, wall.Spec) error) (wall.ResourceFactoryV1, error) {
	if publish == nil {
		return nil, fmt.Errorf("the VM resource factory needs a trusted live manifest publisher")
	}
	return &vmResourcesV1{makeDriver: func() (wall.Driver, error) {
		return chv.New(chv.Options{Facts: microVMFacts, Identity: microVMIdentity, Domain: microVMStoreDomain, Reference: publish, Diagnostic: recordVMDiagnostic, ForkDiskV1: withOriginalForkDiskDataV1, ReservationV2: withOriginalCheckpointReservationV2, ReservationSelectedV2: hasOriginalCheckpointReservationV2})
	}}, nil
}

func (f *vmResourcesV1) resolve() (wall.Driver, error) {
	if f.makeDriver != nil {
		return f.makeDriver()
	}
	return f.driver, nil
}

func (*vmResourcesV1) Version() int { return 1 }

func (f *vmResourcesV1) Build(ctx context.Context) (wall.Sandbox, error) {
	spec, err := expectedVM(ctx, "")
	if err != nil {
		return nil, err
	}
	driver, err := f.resolve()
	if err != nil {
		return nil, vmResourceError(err)
	}
	builder, ok := driver.(interface {
		Build(context.Context, wall.Spec) (wall.Sandbox, error)
	})
	if !ok {
		return nil, UserError("vm_prepare", "the registered VM driver has no version1 resource builder", "Update the host helper before starting the session.")
	}
	sandbox, err := builder.Build(ctx, spec)
	return sandbox, vmResourceError(err)
}

func (f *vmResourcesV1) Prepare(ctx context.Context) (wall.Sandbox, error) {
	spec, err := expectedVM(ctx, "")
	if err != nil {
		return nil, err
	}
	driver, err := f.resolve()
	if err != nil {
		return nil, vmResourceError(err)
	}
	sandbox, err := driver.Prepare(ctx, spec)
	return sandbox, vmResourceError(err)
}

func (f *vmResourcesV1) Open(ctx context.Context, sessionDir string) (wall.Sandbox, error) {
	if _, err := expectedVM(ctx, sessionDir); err != nil {
		return nil, err
	}
	driver, err := f.resolve()
	if err != nil {
		return nil, vmResourceError(err)
	}
	sandbox, err := driver.Open(ctx, sessionDir)
	return sandbox, vmResourceError(err)
}

func expectedVM(ctx context.Context, sessionDir string) (wall.Spec, error) {
	if err := ctx.Err(); err != nil {
		return wall.Spec{}, err
	}
	spec, ok := wall.ExpectedSpec(ctx)
	if !ok || spec.VM == nil || spec.VM.Version != 1 {
		return wall.Spec{}, UserError("vm_expected", "the microvm needs a validated version1 trusted start plan", "Validate the recorded authenticated Need, or refresh it from the client before starting this session.")
	}
	if !validID(spec.ID) || spec.SessionDir != paths.SessionDir(spec.ID) || sessionDir != "" && spec.SessionDir != sessionDir {
		return wall.Spec{}, UserError("vm_expected", "the expected microvm plan names another session", "Start the session again with its original client-authorised identity.")
	}
	if err := checkOriginalSavedImageNeedV1(ctx, spec); err != nil {
		return wall.Spec{}, err
	}
	return spec, nil
}

func vmResourceError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return UserError("vm_prepare", fmt.Sprintf("prepare the microvm: %v", err), "Check the host doctor, runtime files and session log before starting again.")
}
