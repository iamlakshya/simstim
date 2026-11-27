//go:build linux

package host

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"

	"github.com/iamlakshya/simstim/internal/supply"
	"golang.org/x/sys/unix"
)

// This proves caller resource/report separation on actual native objects. It
// is not a systemd manager, stopped-session or full lifecycle positive proof.
func TestActualManagerCallerRetirementDoesNotParkCancelledOutcomeV4(t *testing.T) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fds[1])
	file := os.NewFile(uintptr(fds[0]), "original-caller-resource")
	connection, err := net.FileConn(file)
	if closed := file.Close(); err != nil || closed != nil {
		t.Fatal(err, closed)
	}
	lifetime, cancel := context.WithCancel(context.Background())
	manager := &nativeUserManagerV1{ctx: lifetime, cancel: cancel}
	manager.connection = newNativeManagerTransportV1(connection.(*net.UnixConn), manager.retirement.descriptorOwnerV4())
	request, stop := context.WithCancel(context.Background())
	stop()
	profile := supply.CheckFileCloseThreadV1()
	diagnostic, unknown := retireNativeManagerV4(request, manager)
	if !errors.Is(diagnostic, context.Canceled) {
		t.Fatal("actual request outcome was discarded", diagnostic)
	}
	candidate := &AuxiliaryBootCandidateV1{manager: manager, managerDiagnostic: diagnostic}
	closed := candidate.Close()
	if profile == nil {
		if unknown != nil || closed != nil {
			t.Fatal("known original retirement parked the actual candidate", unknown, closed)
		}
	} else if unknown == nil || closed == nil {
		t.Fatal("unsupported native profile was promoted into successful cleanup")
	}
	if !errors.Is(candidate.CloseDiagnosticsV4(), context.Canceled) {
		t.Fatal("physical advancement lost the original semantic outcome")
	}
	if candidate.Challenge() != "" {
		t.Fatal("cleanup facts authenticated an auxiliary source")
	}
	owner := &OwnedSupervisorLease{managerCleanup: manager, managerDiagnostic: diagnostic}
	ownedClose := owner.CloseWithError()
	if profile == nil && ownedClose != nil {
		t.Fatal("known manager retirement kept the actual owner parked", ownedClose)
	}
	if profile != nil && ownedClose == nil {
		t.Fatal("unknown manager retirement released the actual owner")
	}
	if !errors.Is(owner.ManagerCleanupDiagnosticsV4(), context.Canceled) {
		t.Fatal("original owner lost semantic close outcome")
	}
}
