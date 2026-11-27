//go:build linux

package host

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

// This fixture owns only a local socket, directory and eventfd. It exercises
// physical Close retry and grants no authenticated manager or scope source.
func TestPurgeUnstartedScopeCannotInferNativeRetirementFromErrClosedV4(t *testing.T) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fds[1])
	file := os.NewFile(uintptr(fds[0]), "actual-test-owned-manager-transport")
	connection, err := net.FileConn(file)
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	manager := &nativeUserManagerV1{ctx: ctx, cancel: cancel, root: root}
	manager.connection = newNativeManagerTransportV1(connection.(*net.UnixConn), manager.retirement.descriptorOwnerV4())
	fd, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		_ = manager.Close()
		t.Fatal(err)
	}
	adoption := os.NewFile(uintptr(fd), "actual-test-original-unstarted-latch")
	scope := &purgeAttemptScopeV3{owner: &purgeAttemptExecV1{}, manager: manager, adoption: adoption}
	defer manager.Close()
	defer adoption.Close()
	// Model a lost acknowledgement on this SAME already-closed object.
	if err := adoption.Close(); err != nil {
		t.Fatal(err)
	}
	if err := scope.closeUnstartedV3(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("lost close acknowledgement did not retain original cleanup", err)
	}
	if scope.unstarted == nil || scope.unstarted.scope != scope || scope.unstarted.adoption != adoption || scope.unstarted.manager != manager {
		t.Fatal("the initial error lost its actual original native objects")
	}
	if scope.closed {
		t.Fatal("a lost acknowledgement was reported as a completed constructor")
	}
	if err := scope.closeUnstartedV3(); err == nil {
		t.Fatal("a second Close replaced the original Unknown with a Go closed-object observation")
	}
	if scope.closed {
		t.Fatal("untracked ErrClosed acquired actual native constructor retirement")
	}
}
