//go:build linux

package host

import (
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/sys/unix"
)

func retainedStartupPairV4(t *testing.T) (*StartupLineV1, *net.UnixConn) {
	t.Helper()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	var ends [2]*net.UnixConn
	for i, fd := range pair {
		file := os.NewFile(uintptr(fd), "retained-startup-fixture")
		conn, err := net.FileConn(file)
		if err := errors.Join(err, file.Close()); err != nil {
			t.Fatal(err)
		}
		ends[i] = conn.(*net.UnixConn)
		t.Cleanup(func() { _ = conn.Close() })
		if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	line, err := NewStartupLineV1(ends[0])
	if err != nil {
		t.Fatal(err)
	}
	// This fixture owns real transport/resources only. It issues no Source,
	// native helper identity, operation, startup or execution authority.
	return line, ends[1]
}

func TestStartupRetirementOwnerIsImmutableBeforeFirstRead(t *testing.T) {
	line, sender := retainedStartupPairV4(t)
	owner := &nativeDescriptorRetirementsV1{}
	if err := line.attachNativeRetirementV4(owner); err != nil {
		t.Fatal(err)
	}
	if err := line.attachNativeRetirementV4(&nativeDescriptorRetirementsV1{}); err == nil {
		t.Fatal("a second owner replaced original resource custody")
	}
	if err := sender.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := line.ReadFrame(); err == nil {
		t.Fatal("an ended native transport decoded a frame")
	}
	if line.bytes != 0 || !line.nativeReadStarted {
		t.Fatal("an empty first read did not seal owner attachment")
	}
	if err := line.attachNativeRetirementV4(owner); err == nil || line.nativeRetirement != owner {
		t.Fatal("a late owner attachment changed original custody")
	}
}

func TestStartupRejectionKeepsFirstNativeCloseFacts(t *testing.T) {
	line, sender := retainedStartupPairV4(t)
	owner := &nativeDescriptorRetirementsV1{}
	if err := line.attachNativeRetirementV4(owner); err != nil {
		t.Fatal(err)
	}
	file, err := os.CreateTemp(t.TempDir(), "frame-right-")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := proto.SendUnixFrameV1(sender, proto.TRequest, []byte(`{"id":1,"op":"unexpected"}`), []*os.File{file}); err != nil {
		t.Fatal(err)
	}
	if _, err := line.ReadFrame(); err != nil {
		t.Fatal(err)
	}
	if len(line.files) != 1 {
		t.Fatal("the actual received frame lost its pending object")
	}
	received := line.files[0]
	if err := line.RejectAncillary(); err == nil {
		t.Fatal("rejection became a native-role acceptance")
	}
	if err := owner.checkFilesV4([]*os.File{received}); err != nil {
		t.Fatal("rejection lost its original first-close facts", err)
	}
	if _, err := received.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("the original pending file did not close", err)
	}
}

func TestStartupLowerReceiveFailureRetainsActualPrefix(t *testing.T) {
	line, sender := retainedStartupPairV4(t)
	owner := &nativeDescriptorRetirementsV1{}
	if err := line.attachNativeRetirementV4(owner); err != nil {
		t.Fatal(err)
	}
	file, err := os.CreateTemp(t.TempDir(), "rejected-prefix-")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := proto.SendUnixFrameV1(sender, proto.TRequest, []byte(`{"id":1}`), []*os.File{file, file, file}); err != nil {
		t.Fatal(err)
	}
	if _, err := line.ReadFrame(); err == nil {
		t.Fatal("the exact ordinary role bound accepted a wider native vector")
	}
	files := line.receiveRetention.FailedFilesV1()
	if len(files) == 0 {
		t.Fatal("the failed lower materialisation discarded its actual prefix")
	}
	if err := owner.checkFilesV4(files); err != nil {
		t.Fatal("lower failure closed resources before this original owner", err)
	}
}

func TestSuccessfulStartupClaimDoesNotRetireAtTheReader(t *testing.T) {
	line, sender := retainedStartupPairV4(t)
	owner := &nativeDescriptorRetirementsV1{}
	if err := line.attachNativeRetirementV4(owner); err != nil {
		t.Fatal(err)
	}
	file, err := os.CreateTemp(t.TempDir(), "claimed-right-")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := proto.SendUnixFrameV1(sender, proto.TRequest, []byte(`{"id":1}`), []*os.File{file}); err != nil {
		t.Fatal(err)
	}
	if _, err := line.ReadFrame(); err != nil {
		t.Fatal(err)
	}
	claimed, _, err := line.takeLoanFile()
	if err != nil {
		t.Fatal(err)
	}
	defer claimed.Close()
	if err := owner.checkFilesV4([]*os.File{claimed}); err == nil {
		t.Fatal("taking a resource fabricated a first native Close fact")
	}
	if len(line.receiveRetention.FailedFilesV1()) != 0 {
		t.Fatal("the original failed-batch retention adopted a successful claim")
	}
}
