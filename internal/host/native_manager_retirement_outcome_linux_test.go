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

func TestNativeManagerRetirementSeparatesCancelledOutcomeV4(t *testing.T) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fds[1])
	file := os.NewFile(uintptr(fds[0]), "actual-unused-manager-resource")
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
		t.Fatal("the original cancelled outcome was lost", diagnostic)
	}
	if profile == nil && unknown != nil {
		t.Fatal("request cancellation changed known original native retirement into Unknown", unknown)
	}
	if profile != nil && unknown == nil {
		t.Fatal("an unsupported actual native profile became successful cleanup")
	}
	if err := manager.checkNativeManager(); err == nil {
		t.Fatal("resource retirement became manager admission")
	}
}
