//go:build linux

package host

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"golang.org/x/sys/unix"
)

func TestOrdinaryStoppedCannotOriginateFromMetadata(t *testing.T) {
	for _, operation := range []*SessionOperation{nil, {}, {id: "abcd2345"}} {
		if source, err := StopOrdinarySessionForOperationV1(context.Background(), operation); source != nil || err == nil {
			t.Fatal("data-only operation issued ordinary stopped ownership")
		}
	}
	for _, source := range []*StoppedOrdinarySessionV1{nil, {}, {ordinaryHolderCaptureV2: &ordinaryHolderCaptureV2{operation: &SessionOperation{id: "abcd2345"}, settled: true}}} {
		if err := source.Check(context.Background(), &SessionOperation{id: "abcd2345"}); err == nil {
			t.Fatal("status/boolean became the original ordinary stop source")
		}
	}
}

func TestOrdinaryHolderProofRequiresSameLockedOpenDescription(t *testing.T) {
	name := filepath.Join(t.TempDir(), "holder.lock")
	file, err := os.OpenFile(name, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	fd, err := unix.FcntlInt(file.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := os.NewFile(uintptr(fd), "same-real-holder-ofd")
	defer duplicate.Close()
	if err := checkOrdinaryHolderFlockV1(duplicate); err != nil {
		t.Fatal("actual duplicate lost original lock observation", err)
	}
	reopened, err := os.OpenFile(name, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := checkOrdinaryHolderFlockV1(reopened); err == nil {
		t.Fatal("same inode independently reopened became original holder OFD")
	}
}

func TestOrdinaryCompletionNeedsActualNativeJoinWrite(t *testing.T) {
	fd, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(fd), "real-join-completion")
	defer file.Close()
	if err := ordinaryJoinLatchV1(file, false); err != nil {
		t.Fatal(err)
	}
	if err := ordinaryJoinLatchV1(file, true); err == nil {
		t.Fatal("uncompleted join supplied native completion")
	}
	var value [8]byte
	binary.NativeEndian.PutUint64(value[:], 1)
	if _, err := file.Write(value[:]); err != nil {
		t.Fatal(err)
	}
	if err := ordinaryJoinLatchV1(file, true); err != nil {
		t.Fatal(err)
	}
	if err := ordinaryJoinLatchV1(file, true); err != nil {
		t.Fatal("checking consumed the native completion", err)
	}
	regular, err := os.CreateTemp(t.TempDir(), "eventfd-looking-")
	if err != nil {
		t.Fatal(err)
	}
	defer regular.Close()
	_, _ = regular.WriteString("eventfd-count: 1\neventfd-id: 1\n")
	if err := ordinaryJoinLatchV1(regular, true); err == nil {
		t.Fatal("regular file supplied native completion")
	}
}

func TestOriginalMainPidfdCannotSignalAnotherLiveProcessAfterWait(t *testing.T) {
	original := exec.Command("/bin/sh", "-c", "exit 0")
	main := prepareHolderMainV1(original)
	if err := original.Start(); err != nil {
		t.Fatal(err)
	}
	if main.pidfd < 0 {
		_ = original.Wait()
		t.Skip("this kernel supplies no atomic child pidfd; ordinary stopped proof is unsupported")
	}
	defer unix.Close(main.pidfd)
	if err := original.Wait(); err != nil {
		t.Fatal(err)
	}
	unrelated := exec.Command("/bin/sh", "-c", "exec sleep 60")
	if err := unrelated.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unrelated.Process.Kill(); _ = unrelated.Wait() }()
	if err := signalHolderMainV1(main, syscall.SIGKILL); !errors.Is(err, unix.ESRCH) {
		t.Fatal("exited original creation was not retained", err)
	}
	if err := unrelated.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatal("original main signal affected unrelated process", err)
	}
}

func TestOwnedMainJoinClosesOriginalPTYAndJoinsWaitAndReader(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "60")
	main := prepareHolderMainV1(cmd)
	terminal, err := pty.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if main.pidfd >= 0 {
		defer unix.Close(main.pidfd)
	}
	h := &Holder{cmd: cmd, main: main, ptmx: terminal, waitDone: make(chan struct{}), readDone: make(chan struct{})}
	go func() { _, _ = io.Copy(io.Discard, terminal); close(h.readDone) }()
	go func() { _ = cmd.Wait(); close(h.waitDone) }()
	done := make(chan error, 1)
	go func() { done <- h.joinOwnedMainV1() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("original main and PTY join did not complete")
	}
	select {
	case <-h.waitDone:
	default:
		t.Fatal("main Wait remains live")
	}
	select {
	case <-h.readDone:
	default:
		t.Fatal("original PTY reader remains live")
	}
	if err := h.joinOwnedMainV1(); err != nil {
		t.Fatal("second join changed the original owned lifetime", err)
	}
}

func TestOrdinarySocketClosurePreservesReplacement(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "session.sock")
	second := filepath.Join(root, "hook.sock")
	listen := func(name string) *net.UnixListener {
		listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: name, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		listener.SetUnlinkOnClose(false)
		if err := os.Chmod(name, 0o600); err != nil {
			t.Fatal(err)
		}
		return listener
	}
	ln, hook := listen(first), listen(second)
	defer ln.Close()
	defer hook.Close()
	// Populate only the endpoint primitive from genuine listener creations;
	// this test never constructs a positive stopped session subject.
	owner := &ownedOrdinaryHolderV1{}
	for _, name := range []string{first, second} {
		directory, err := os.OpenRoot(root)
		if err != nil {
			t.Fatal(err)
		}
		defer directory.Close()
		reference, err := cachefs.CaptureRoot(directory)
		if err != nil {
			t.Fatal(err)
		}
		info, err := directory.Lstat(filepath.Base(name))
		if err != nil {
			t.Fatal(err)
		}
		owner.endpoints = append(owner.endpoints, &ordinaryEndpointV1{root: directory, rootRef: reference, name: filepath.Base(name), original: info})
	}
	if err := os.Remove(first); err != nil {
		t.Fatal(err)
	}
	replacement := listen(first)
	defer replacement.Close()
	info, err := os.Lstat(first)
	if err != nil {
		t.Fatal(err)
	}
	h := &Holder{ordinary: owner, ln: ln, hookLn: hook}
	if err := h.closeOriginalOrdinarySocketsV1(); err == nil {
		t.Fatal("replaced socket was accepted as original endpoint")
	}
	current, err := os.Lstat(first)
	if err != nil || !os.SameFile(info, current) {
		t.Fatal("replacement socket was removed", err)
	}
}
