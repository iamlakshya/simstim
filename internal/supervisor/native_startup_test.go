package supervisor

import (
	"context"
	"net"
	"testing"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/wall"
	"github.com/iamlakshya/simstim/internal/wall/microvm"
)

type unissuedVMRuntime struct{ bindings int }

func (*unissuedVMRuntime) Start(context.Context) (net.Conn, error) {
	panic("unauthorised runtime started")
}
func (*unissuedVMRuntime) Wait() error { return nil }
func (*unissuedVMRuntime) Kill() error { return nil }
func (r *unissuedVMRuntime) NativeBindingV1(context.Context, net.Conn) (*microvm.NativeBindingV1, error) {
	r.bindings++
	return nil, nil
}

func TestVMMetadataCannotEnterFactoryOrConsumeNativeBinding(t *testing.T) {
	for _, driver := range []string{"chv", "firecracker"} {
		t.Run(driver, func(t *testing.T) {
			// Even a matching public expected-spec context has no original OP or
			// native lease. It must refuse before touching the engine producer.
			owner := wall.WithExpectedSpec(t.Context(), wall.Spec{ID: "abcdefgh", VM: &wall.VMSpec{}})
			s := &Supervisor{id: "abcdefgh", lifetime: owner, m: &host.Manifest{ID: "abcdefgh", Wall: &host.WallRecord{Level: "microvm", Driver: driver}}}
			if _, err := s.sandboxForRun(); err == nil {
				t.Fatal("metadata entered a runtime factory")
			}
			runtime := &unissuedVMRuntime{}
			if err := s.retainVMStart(t.Context(), runtime, nil); err == nil {
				t.Fatal("metadata admitted VM prepare")
			}
			if runtime.bindings != 0 {
				t.Fatal("an unissued request consumed native binding")
			}
		})
	}
}

func TestVMRuntimeAdapterDoesNotInventNativeBinding(t *testing.T) {
	adapter := vmRuntime{}
	if binding, err := adapter.NativeBindingV1(t.Context(), nil); err == nil || binding != nil {
		t.Fatal("an absent concrete engine issued native binding")
	}
}
