//go:build linux

package process

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/wall"
	"golang.org/x/sys/unix"
)

func TestInitPollRequiresKernelExitReadiness(t *testing.T) {
	old := pollInit
	defer func() { pollInit = old }()
	for _, tc := range []struct {
		name   string
		n      int
		events int16
		err    error
		ready  bool
	}{
		{"POLLIN", 1, unix.POLLIN, nil, true},
		{"POLLIN HUP", 1, unix.POLLIN | unix.POLLHUP, nil, true},
		{"HUP only", 1, unix.POLLHUP, nil, false},
		{"NVAL", 1, unix.POLLNVAL, nil, false},
		{"ERR with IN", 1, unix.POLLERR | unix.POLLIN, nil, false},
		{"unrequested readiness", 1, unix.POLLOUT | unix.POLLIN, nil, false},
		{"poll failure", -1, 0, unix.EIO, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pollInit = func(fds []unix.PollFd, timeout int) (int, error) { fds[0].Revents = tc.events; return tc.n, tc.err }
			if err := waitInitExit(17); (err == nil) != tc.ready {
				t.Fatalf("wait accepted=%v: %v", tc.ready, err)
			}
			if got := initExited(17); got != tc.ready {
				t.Fatalf("nonblocking accepted=%v, want %v", got, tc.ready)
			}
		})
	}
	calls := 0
	pollInit = func(fds []unix.PollFd, timeout int) (int, error) {
		calls++
		if calls == 1 {
			return -1, unix.EINTR
		}
		if calls == 2 {
			return 0, nil
		}
		fds[0].Revents = unix.POLLIN
		return 1, nil
	}
	if err := waitInitExit(17); err != nil || calls != 3 {
		t.Fatalf("retry count=%d: %v", calls, err)
	}
}

func TestOwnedPIDFDHelper(t *testing.T) {
	if os.Getenv("OWNED_PIDFD_HELPER") != "1" {
		return
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

func ownedChild(t *testing.T) (*exec.Cmd, *os.File, int) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestOwnedPIDFDHelper$")
	cmd.Env = append(os.Environ(), "OWNED_PIDFD_HELPER=1")
	cmd.Stdin = reader
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	reader.Close()
	fd, err := unix.PidfdOpen(cmd.Process.Pid, 0)
	if err != nil {
		writer.Close()
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatal(err)
	}
	t.Cleanup(func() { writer.Close(); cmd.Process.Kill(); cmd.Wait(); unix.Close(fd) })
	return cmd, writer, fd
}

func TestOwnedPIDFDProvesOrdinaryChildExit(t *testing.T) {
	cmd, writer, fd := ownedChild(t)
	if initExited(fd) {
		t.Fatal("live child reported exited")
	}
	writer.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := waitInitExit(fd); err != nil || !initExited(fd) {
		t.Fatalf("kernel did not prove owned child exit: %v", err)
	}
}

func TestWaitErrorRetainsLiveInitAndRefusesDestroy(t *testing.T) {
	_, _, originalFD := ownedChild(t)
	fd, err := unix.FcntlInt(uintptr(originalFD), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "process.json"), []byte("owned"), 0600); err != nil {
		t.Fatal(err)
	}
	prepared, err := newSandbox(wall.Spec{SessionDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	s := prepared.(*sandbox)
	launcher := exec.Command(os.Args[0], "-test.run=^TestOwnedPIDFDHelper$")
	if err := launcher.Start(); err != nil {
		t.Fatal(err)
	}
	s.cmd, s.pidfd = launcher, fd
	oldPoll, oldSignal := pollInit, signalInit
	defer func() { pollInit, signalInit = oldPoll, oldSignal }()
	pollInit = func([]unix.PollFd, int) (int, error) { return -1, unix.EIO }
	signalInit = func(int, unix.Signal, *unix.Siginfo, int) error { return nil }
	s.reap(launcher)
	if result := s.Wait(); !errors.Is(result.Err, unix.EIO) {
		t.Fatalf("wait lost uncertainty: %+v", result)
	}
	if s.closed || s.pidfd != fd || s.CleanExit() {
		t.Fatal("uncertain init was released or clean")
	}
	if err := s.Destroy(); err == nil {
		t.Fatal("destroy accepted possibly live init")
	}
	if _, err := os.Stat(filepath.Join(dir, "process.json")); err != nil {
		t.Fatal("destroy removed runtime while init possibly live", err)
	}
	if err := unix.PidfdSendSignal(fd, 0, nil, 0); err != nil {
		t.Fatal("init no longer alive", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Stop(ctx, time.Second); !errors.Is(err, unix.EIO) {
		t.Fatalf("stop forgot prior wait failure: %v", err)
	}
	pollInit, signalInit = oldPoll, oldSignal
	if err := s.Kill(); !errors.Is(err, unix.EIO) {
		t.Fatalf("retry lost original failure: %v", err)
	}
	if s.pidfd >= 0 || !s.closed || s.CleanExit() {
		t.Fatal("retry did not prove closure or erased failure")
	}
	if err := s.Destroy(); err != nil {
		t.Fatal(err)
	}
}

func TestDestroyKeepsSessionDataAndRefusesRuntimeReplacement(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"process.json", "manifest.json", "holder.log", "work", "state"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	prepared, err := newSandbox(wall.Spec{SessionDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	s := prepared.(*sandbox)
	if err := os.Rename(filepath.Join(dir, "process.json"), filepath.Join(dir, "original")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "process.json"), []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Destroy(); err == nil {
		t.Fatal("destroy deleted replacement")
	}
	got, err := os.ReadFile(filepath.Join(dir, "process.json"))
	if err != nil || string(got) != "replacement" {
		t.Fatalf("replacement changed: %q %v", got, err)
	}
	if err := os.Remove(filepath.Join(dir, "process.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "original"), filepath.Join(dir, "process.json")); err != nil {
		t.Fatal(err)
	}
	if err := s.Destroy(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"manifest.json", "holder.log", "work", "state"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("removed %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "process.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("owned runtime remains", err)
	}
}

func TestFailedInitKillDoesNotWaitOrReleaseLiveOwnership(t *testing.T) {
	_, _, originalFD := ownedChild(t)
	fd, err := unix.FcntlInt(uintptr(originalFD), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := newSandbox(wall.Spec{SessionDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	s := prepared.(*sandbox)
	launcher := exec.Command(os.Args[0], "-test.run=^TestOwnedPIDFDHelper$")
	if err := launcher.Start(); err != nil {
		t.Fatal(err)
	}
	s.cmd, s.pidfd = launcher, fd
	oldPoll, oldSignal := pollInit, signalInit
	defer func() { pollInit, signalInit = oldPoll, oldSignal }()
	polls := 0
	pollInit = func(_ []unix.PollFd, timeout int) (int, error) {
		polls++
		if timeout != 0 {
			return -1, unix.EIO
		}
		return 0, nil
	}
	signalInit = func(int, unix.Signal, *unix.Siginfo, int) error { return unix.EPERM }
	s.reap(launcher)
	if result := s.Wait(); !errors.Is(result.Err, unix.EPERM) || errors.Is(result.Err, unix.EIO) || polls != 1 {
		t.Fatalf("failed kill was hidden or waited: %+v polls=%d", result, polls)
	}
	if s.closed || s.pidfd != fd || s.CleanExit() {
		t.Fatal("failed kill released live ownership")
	}
	if err := s.Destroy(); err == nil {
		t.Fatal("destroy accepted failed kill")
	}
	pollInit, signalInit = oldPoll, oldSignal
	if err := s.Kill(); !errors.Is(err, unix.EPERM) {
		t.Fatal("physical retry lost original failed kill", err)
	}
	if err := s.Destroy(); err != nil {
		t.Fatal(err)
	}
}
