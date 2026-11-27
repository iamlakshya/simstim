//go:build linux

package host

import (
	"context"
	"runtime"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/wall"
	"github.com/iamlakshya/simstim/internal/wall/gvisor"
)

type gvisorRegistrationV1 struct{}

func init() {
	if err := wall.Register(&gvisorRegistrationV1{}); err != nil {
		panic(err)
	}
}

func (*gvisorRegistrationV1) Name() string      { return "gvisor" }
func (*gvisorRegistrationV1) Level() wall.Level { return wall.GVisor }
func (r *gvisorRegistrationV1) Probe(ctx context.Context) wall.Probe {
	driver, err := NewGvisorDriver()
	if err != nil {
		return wall.Probe{Driver: r.Name(), Level: r.Level(), Reason: err.Error(), Fix: "Check the host's private runtime storage."}
	}
	return driver.Probe(ctx)
}
func (*gvisorRegistrationV1) Prepare(ctx context.Context, spec wall.Spec) (wall.Sandbox, error) {
	driver, err := NewGvisorDriver()
	if err != nil {
		return nil, err
	}
	return driver.Prepare(ctx, spec)
}
func (*gvisorRegistrationV1) Open(ctx context.Context, dir string) (wall.Sandbox, error) {
	driver, err := NewGvisorDriver()
	if err != nil {
		return nil, err
	}
	return driver.Open(ctx, dir)
}

// RuntimeCompatibility is a data projection after the runtime service has
// checked the supplied original closure's files. It supplies neither physical
// Probe.OK nor the private original boot/probe purpose required for execution.
func (*gvisorRegistrationV1) RuntimeCompatibility(manifest supply.Manifest) (api.RuntimeDriver, bool) {
	if _, err := NewGvisorDriver(); err != nil {
		return api.RuntimeDriver{}, false
	}
	if _, err := gvisor.RuntimeItemsV1(manifest, runtime.GOARCH); err != nil {
		return api.RuntimeDriver{}, false
	}
	return api.RuntimeDriver{Name: "gvisor", Arch: runtime.GOARCH, KernelMin: "5.6", Verified: true}, true
}
