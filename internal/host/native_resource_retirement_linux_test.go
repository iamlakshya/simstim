//go:build linux

package host

import (
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/supply"
	"golang.org/x/sys/unix"
)

func TestNativeRetirementKeepsOriginalFirstFactsV4(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "actual-first-close-")
	if err != nil {
		t.Fatal(err)
	}
	owner := &nativeDescriptorRetirementsV1{}
	profile := supply.CheckFileCloseThreadV1()
	first := owner.closeFilesV4([]*os.File{file})
	if profile == nil {
		if first != nil || owner.checkFilesV4([]*os.File{file}) != nil {
			t.Fatal("supported actual first close has no retirement", first)
		}
	} else if owner.checkFilesV4([]*os.File{file}) == nil {
		t.Fatal("an unsupported actual close profile supplied native retirement")
	}
	again := owner.closeFilesV4([]*os.File{file})
	if (first == nil) != (again == nil) || first != nil && again.Error() != first.Error() {
		t.Fatal("retry replaced the original actual close facts", first, again)
	}
	replacement, err := os.CreateTemp(t.TempDir(), "unrelated-open-description-")
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	if err := owner.checkFilesV4([]*os.File{replacement}); err == nil {
		t.Fatal("a new live object acquired an old native close receipt")
	}
	if _, err := replacement.Stat(); err != nil {
		t.Fatal("checking original facts closed another actual object", err)
	}
}

func TestNativeRetirementCannotUseErrClosedOrASecondCloseV4(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "closed-without-proof-")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	owner := &nativeDescriptorRetirementsV1{}
	for range 2 {
		if err := owner.closeFilesV4([]*os.File{file}); err == nil {
			t.Fatal("ErrClosed became an original native retirement proof")
		}
		if err := owner.checkFilesV4([]*os.File{file}); err == nil {
			t.Fatal("a retry replaced Unknown with a closed-object observation")
		}
	}
}

func TestNativeManagerTransportJoinsActualReadBeforeCloseV4(t *testing.T) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fds[1])
	file := os.NewFile(uintptr(fds[0]), "actual-manager-transport-test")
	connection, err := net.FileConn(file)
	if err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	_ = file.Close()
	core := &nativeDescriptorRetirementsV1{}
	transport := newNativeManagerTransportV1(connection.(*net.UnixConn), core)
	readDone := make(chan error, 1)
	go func() { var data [1]byte; _, err := transport.Read(data[:]); readDone <- err }()
	deadline := time.Now().Add(5 * time.Second)
	for transport.read.TryLock() {
		transport.read.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("the actual native read never entered its owned gate")
		}
		time.Sleep(time.Millisecond)
	}
	profile := supply.CheckFileCloseThreadV1()
	closed := transport.Close()
	select {
	case err := <-readDone:
		if err == nil {
			t.Fatal("native close did not interrupt the original physical reader")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the original read did not physically return after native close joined its users")
	}
	if profile == nil {
		if closed != nil || transport.checkNativeRetiredV1() != nil {
			t.Fatal("supported actual Unix close has no first native facts", closed)
		}
	} else if transport.checkNativeRetiredV1() == nil {
		t.Fatal("a filtered or unknown actual close thread supplied Unix retirement")
	}
	if _, err := transport.Write([]byte{1}); !errors.Is(err, net.ErrClosed) {
		t.Fatal("retired transport admitted a new write", err)
	}
}

func TestNativeResourcePoolCannotBeReplacedV4(t *testing.T) {
	owner := &nativeStartupRetirementsV1{}
	first, other := &nativeDescriptorRetirementsV1{}, &nativeDescriptorRetirementsV1{}
	if err := owner.bindDescriptorOwnerV4(first); err != nil {
		t.Fatal(err)
	}
	if err := owner.bindDescriptorOwnerV4(other); err == nil {
		t.Fatal("a later caller replaced the original actual retirement pool")
	}
	if owner.descriptorOwnerV4() != first {
		t.Fatal("original native facts were replaced")
	}
}
