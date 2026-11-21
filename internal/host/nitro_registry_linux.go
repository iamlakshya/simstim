//go:build linux

package host

import (
	"context"
	"fmt"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/wall"
	"github.com/iamlakshya/simstim/internal/wall/nitro"
)

var nitroDriver nitroRegistration

func init() {
	if err := wall.Register(&nitroDriver); err != nil {
		panic(err)
	}
}

// NewNitroDriver binds the real shared engine to the same host facts,
// native storage domain and protected publisher as the other VM backend.
func NewNitroDriver() (*nitro.Driver, error) {
	return nitro.New(nitro.Options{Facts: microVMFacts, Identity: microVMIdentity,
		Domain: microVMStoreDomain, Reference: recordVMAuthority, Diagnostic: recordVMDiagnostic})
}

type nitroRegistration struct{}

func (*nitroRegistration) Name() string      { return "nitro" }
func (*nitroRegistration) Level() wall.Level { return wall.MicroVM }

func (r *nitroRegistration) Probe(ctx context.Context) wall.Probe {
	driver, err := NewNitroDriver()
	if err != nil {
		return wall.Probe{Driver: r.Name(), Level: r.Level(), Reason: err.Error(), Fix: "Check the host's private state and data folders."}
	}
	return driver.Probe(ctx)
}

func (*nitroRegistration) Prepare(ctx context.Context, spec wall.Spec) (wall.Sandbox, error) {
	driver, err := NewNitroDriver()
	if err != nil {
		return nil, err
	}
	return driver.Prepare(ctx, spec)
}

func (*nitroRegistration) Open(ctx context.Context, dir string) (wall.Sandbox, error) {
	driver, err := NewNitroDriver()
	if err != nil {
		return nil, err
	}
	return driver.Open(ctx, dir)
}

func (*nitroRegistration) Build(ctx context.Context, spec wall.Spec) (wall.Sandbox, error) {
	driver, err := NewNitroDriver()
	if err != nil {
		return nil, err
	}
	return driver.Build(ctx, spec)
}

func (*nitroRegistration) RuntimeCompatibility(manifest supply.Manifest) (api.RuntimeDriver, bool) {
	driver, err := NewNitroDriver()
	if err != nil {
		return api.RuntimeDriver{}, false
	}
	return driver.RuntimeCompatibility(manifest)
}

// NitroResourcesV1 selects the actual registered factory. The original
// protected operation and boot plan remain separate requirements of its caller.
func NitroResourcesV1() wall.ResourceFactoryV1 {
	return &vmResourcesV1{driver: &nitroDriver}
}

// NitroResourcesWithPublisherV1 preserves the live supervisor's one
// protected manifest writer through the shared engine's image/runtime lease.
func NitroResourcesWithPublisherV1(publish func(context.Context, wall.Spec) error) (wall.ResourceFactoryV1, error) {
	if publish == nil {
		return nil, fmt.Errorf("the VM resource factory needs a trusted live manifest publisher")
	}
	return &vmResourcesV1{makeDriver: func() (wall.Driver, error) {
		return nitro.New(nitro.Options{Facts: microVMFacts, Identity: microVMIdentity,
			Domain: microVMStoreDomain, Reference: publish, Diagnostic: recordVMDiagnostic})
	}}, nil
}
