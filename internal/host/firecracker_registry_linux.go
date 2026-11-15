//go:build linux

package host

import (
	"context"
	"fmt"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/wall"
	"github.com/iamlakshya/simstim/internal/wall/firecracker"
)

var firecrackerDriver firecrackerRegistration

func init() {
	if err := wall.Register(&firecrackerDriver); err != nil {
		panic(err)
	}
}

// NewFirecrackerDriver binds the real shared engine to the same host facts,
// native storage domain and protected publisher as the other VM backend.
func NewFirecrackerDriver() (*firecracker.Driver, error) {
	return firecracker.New(firecracker.Options{Facts: microVMFacts, Identity: microVMIdentity,
		Domain: microVMStoreDomain, Reference: recordVMAuthority, Diagnostic: recordVMDiagnostic})
}

type firecrackerRegistration struct{}

func (*firecrackerRegistration) Name() string      { return "firecracker" }
func (*firecrackerRegistration) Level() wall.Level { return wall.MicroVM }

func (r *firecrackerRegistration) Probe(ctx context.Context) wall.Probe {
	driver, err := NewFirecrackerDriver()
	if err != nil {
		return wall.Probe{Driver: r.Name(), Level: r.Level(), Reason: err.Error(), Fix: "Check the host's private state and data folders."}
	}
	return driver.Probe(ctx)
}

func (*firecrackerRegistration) Prepare(ctx context.Context, spec wall.Spec) (wall.Sandbox, error) {
	driver, err := NewFirecrackerDriver()
	if err != nil {
		return nil, err
	}
	return driver.Prepare(ctx, spec)
}

func (*firecrackerRegistration) Open(ctx context.Context, dir string) (wall.Sandbox, error) {
	driver, err := NewFirecrackerDriver()
	if err != nil {
		return nil, err
	}
	return driver.Open(ctx, dir)
}

func (*firecrackerRegistration) Build(ctx context.Context, spec wall.Spec) (wall.Sandbox, error) {
	driver, err := NewFirecrackerDriver()
	if err != nil {
		return nil, err
	}
	return driver.Build(ctx, spec)
}

func (*firecrackerRegistration) RuntimeCompatibility(manifest supply.Manifest) (api.RuntimeDriver, bool) {
	driver, err := NewFirecrackerDriver()
	if err != nil {
		return api.RuntimeDriver{}, false
	}
	return driver.RuntimeCompatibility(manifest)
}

// FirecrackerResourcesV1 selects the actual registered factory. The original
// protected operation and boot plan remain separate requirements of its caller.
func FirecrackerResourcesV1() wall.ResourceFactoryV1 {
	return &vmResourcesV1{driver: &firecrackerDriver}
}

// FirecrackerResourcesWithPublisherV1 preserves the live supervisor's one
// protected manifest writer through the shared engine's image/runtime lease.
func FirecrackerResourcesWithPublisherV1(publish func(context.Context, wall.Spec) error) (wall.ResourceFactoryV1, error) {
	if publish == nil {
		return nil, fmt.Errorf("the VM resource factory needs a trusted live manifest publisher")
	}
	return &vmResourcesV1{makeDriver: func() (wall.Driver, error) {
		return firecracker.New(firecracker.Options{Facts: microVMFacts, Identity: microVMIdentity,
			Domain: microVMStoreDomain, Reference: publish, Diagnostic: recordVMDiagnostic})
	}}, nil
}
