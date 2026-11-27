package supervisor

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/wall"
)

type vmRuntimeFixture struct {
	exit    wall.Exit
	starts  int
	clean   bool
	power   error
	pressed bool
}

func (r *vmRuntimeFixture) Start(context.Context) (net.Conn, error) {
	r.starts++
	return nil, nil
}
func (*vmRuntimeFixture) Stop(context.Context, time.Duration) error { return nil }
func (*vmRuntimeFixture) Kill() error                               { return nil }
func (r *vmRuntimeFixture) Wait() wall.Exit                         { return r.exit }
func (*vmRuntimeFixture) Destroy() error                            { return nil }
func (*vmRuntimeFixture) Abilities() wall.Abilities                 { return wall.Abilities{} }
func (r *vmRuntimeFixture) CleanExit() bool                         { return r.clean }
func (r *vmRuntimeFixture) PowerButton(context.Context) error {
	r.pressed = true
	return r.power
}

func TestVMRuntimeWaitRetainsOwnedExitFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		exit wall.Exit
		want string
	}{
		{"wait error", wall.Exit{Err: errors.New("owned VMM wait failed")}, "owned VMM wait failed"},
		{"exit code", wall.Exit{Code: 7}, "code 7"},
		{"signal", wall.Exit{Code: 137, Signal: "killed"}, "signal killed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime := vmRuntime{Sandbox: &vmRuntimeFixture{exit: tc.exit}}
			var wire *proto.Error
			if err := runtime.Wait(); !errors.As(err, &wire) || wire.Code != "sandbox_error" || wire.Hint == "" || !strings.Contains(wire.Message, tc.want) {
				t.Fatalf("owned exit failure was lost: %v", err)
			}
		})
	}
	if err := (vmRuntime{Sandbox: &vmRuntimeFixture{}}).Wait(); err != nil {
		t.Fatalf("a successful owned wait failed: %v", err)
	}
}

func TestVMRuntimeErrorKeepsCancellationAndTypedFailures(t *testing.T) {
	for _, err := range []error{context.Canceled, context.DeadlineExceeded, &proto.Error{Code: "disk_measure", Message: "owned disk gone", Hint: "Resume the session."}} {
		if got := vmRuntimeError("sandbox_boot", err); got != err {
			t.Fatalf("a typed failure changed identity: %v", got)
		}
	}
	var wire *proto.Error
	err := vmRuntimeError("sandbox_boot", &supply.MismatchError{Name: "vmm", What: "SHA-256", Want: "expected", Got: "changed"})
	if !errors.As(err, &wire) || wire.Code != "digest_mismatch" || wire.Hint == "" {
		t.Fatalf("an actual digest failure lost its error code: %v", err)
	}
}

func TestVMRuntimeStartFencesReleasedDiskResources(t *testing.T) {
	driver := &vmRuntimeFixture{}
	for _, disk := range []*diskResourcesV1{nil, {closed: true}} {
		runtime := vmRuntime{Sandbox: driver, disk: disk}
		if _, err := runtime.Start(t.Context()); err == nil || driver.starts != 0 {
			t.Fatalf("a missing or released disk allowed a VM start: %v", err)
		}
	}
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	disk := &diskResourcesV1{volume: root}
	t.Cleanup(func() { _ = disk.Close() })
	runtime := vmRuntime{Sandbox: driver, disk: disk}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := runtime.Start(ctx); !errors.Is(err, context.Canceled) || driver.starts != 0 {
		t.Fatalf("a cancelled start reached the VM driver: %v", err)
	}
	if _, err := runtime.Start(t.Context()); err != nil || driver.starts != 1 || runtime.ownedDiskResourcesV1() != disk {
		t.Fatalf("the adapter did not use its factory-owned resource and driver: %v", err)
	}
	if err := disk.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Start(t.Context()); err == nil || driver.starts != 1 {
		t.Fatalf("the adapter restarted a released runtime: %v", err)
	}
}

func TestVMRuntimeForwardsOwnedCleanExitAndPowerControl(t *testing.T) {
	driver := &vmRuntimeFixture{power: errors.New("power control failed")}
	runtime := vmRuntime{Sandbox: driver}
	if runtime.CleanExit() {
		t.Fatal("the adapter invented a clean owned exit")
	}
	driver.clean = true
	if !runtime.CleanExit() {
		t.Fatal("the actual owned clean exit was hidden")
	}
	if err := runtime.PowerButton(t.Context()); err != driver.power || !driver.pressed {
		t.Fatalf("the adapter lost the owned power control result: %v", err)
	}
	// The base interface hides optional driver evidence. Its absence must
	// not become a successful clean exit or an invented control operation.
	bare := vmRuntime{Sandbox: struct{ wall.Sandbox }{driver}}
	if bare.CleanExit() || bare.PowerButton(t.Context()) == nil {
		t.Fatal("a driver without the optional ownership contracts supplied them")
	}
}
