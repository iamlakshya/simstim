//go:build linux

package host

import (
	"context"

	"github.com/iamlakshya/simstim/internal/wall"
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
