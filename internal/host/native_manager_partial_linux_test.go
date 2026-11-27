//go:build linux

package host

import (
	"context"
	"net"
	"os"
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/sys/unix"
)

func TestPartialNativeManagerCannotObserveOrStartAUnit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := &nativeUserManagerV1{ctx: ctx, cancel: cancel}
	if err := manager.checkNativeManager(); err == nil {
		t.Fatal("unverified manager became an authenticated source")
	}
	if _, err := manager.getProperty(ctx, dbus.ObjectPath("/"), systemdUnitInterface, "Id"); err == nil {
		t.Fatal("partial manager reached its nil native bus")
	}
	if unit, err := manager.startSupervisor(ctx, &Manifest{ID: "a2a2a2a2", Wall: &WallRecord{Level: "none", Driver: "none"}}, "", nil, nil); err == nil || unit != nil {
		t.Fatal("partial manager created a runtime", err)
	}
	candidate := &AuxiliaryBootCandidateV1{primary: "a2a2a2a2", manager: manager}
	if candidate.Challenge() != "" {
		t.Fatal("partial candidate issued a native challenge")
	}
	if _, err := candidate.ReadFrame(); err == nil {
		t.Fatal("partial candidate read a missing physical line")
	}
	if err := candidate.WriteFrame(proto.TResponse, proto.Response{}); err == nil {
		t.Fatal("partial candidate wrote a missing physical line")
	}
	if value, err := candidate.Accept(ctx, proto.Frame{}); err == nil || value != nil {
		t.Fatal("partial manager candidate accepted an OP loan", err)
	}
}

func TestNativeManagerUnknownCloseKeepsOriginalDiagnosticAndFencesUse(t *testing.T) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(fds[0]), "original-manager-test-socket")
	connection, err := net.FileConn(file)
	if closeErr := file.Close(); err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	defer unix.Close(fds[1])
	conn := connection.(*net.UnixConn)
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	manager := &nativeUserManagerV1{ctx: ctx, cancel: cancel}
	manager.connection = newNativeManagerTransportV1(conn, manager.retirement.descriptorOwnerV4())
	first := manager.Close()
	if first == nil {
		t.Fatal("already-closed original socket supplied a false successful cleanup")
	}
	if again := manager.Close(); again == nil || again.Error() != first.Error() {
		t.Fatal("unknown original close lost its diagnostic on retry", again)
	}
	if err := manager.checkNativeManager(); err == nil {
		t.Fatal("an unknown close reauthenticated the original partial manager")
	}
	if ctx.Err() == nil {
		t.Fatal("failed manager cleanup left operational admission open")
	}
}
