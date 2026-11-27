//go:build linux

package host

import (
	"context"
	"testing"

	"github.com/iamlakshya/simstim/internal/wall"
)

func TestMicroVMFactorySelectsOnlyImplementedOriginalBackend(t *testing.T) {
	for _, name := range []string{"chv", "firecracker", "nitro"} {
		factory, err := MicroVMResourcesWithPublisherV1(name, func(context.Context, wall.Spec) error {
			t.Fatal("selecting a factory performed publication")
			return nil
		})
		if err != nil || factory == nil || factory.Version() != 1 {
			t.Fatal("implemented backend lost its actual resource factory", name, err)
		}
		driver, err := factory.(*vmResourcesV1).resolve()
		if err != nil || driver.Name() != name || driver.Level() != wall.MicroVM {
			t.Fatal("factory selected a different actual backend", name, err)
		}
		if factory, err := MicroVMResourcesWithPublisherV1(name, nil); err == nil || factory != nil {
			t.Fatal("a backend manufactured the protected publisher", name)
		}
	}
	for _, name := range []string{"", "none", "process", "gvisor", "other"} {
		if factory, err := MicroVMResourcesWithPublisherV1(name, func(context.Context, wall.Spec) error { return nil }); err == nil || factory != nil {
			t.Fatal("unimplemented or non-VM backend selected a VM factory", name)
		}
	}
}
