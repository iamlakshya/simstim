//go:build linux

package jail

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestPhysicalExitRequiresActualOwnedCreationPidfd(t *testing.T) {
	c := lifecycleCmd(t)
	if c.PhysicalExitV1() {
		t.Fatal("a new command has no exit evidence")
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	if c.pidfd < 0 || c.PhysicalExitV1() {
		t.Fatal("start lost native ownership or claimed exit")
	}
	flags, err := unix.FcntlInt(uintptr(c.pidfd), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatal("creation pidfd is not CLOEXEC", err)
	}
	var ended *ExitError
	if err := c.Wait(); !errors.As(err, &ended) || ended.Code != 7 {
		t.Fatal("actual ordinary owned child wait changed", err)
	}
	if !c.PhysicalExitV1() || c.pidfd != -1 || !c.reaped {
		t.Fatal("actual POLLIN closure was not published")
	}
}

func TestWaitidErrorRetainsLiveOwnerAndRevokesNumericGroupSignal(t *testing.T) {
	c := lifecycleCmd(t)
	c.cmd = exec.Command("/bin/sh", "-c", "cat <&3 >/dev/null; read hold")
	c.cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	stdin, err := c.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	fd := c.pidfd
	t.Cleanup(func() {
		if !c.PhysicalExitV1() {
			_ = c.Signal(os.Kill)
			_ = c.Wait()
		}
	})
	oldWait, oldGroup, oldSignal := ownedWaitid, ownedGroupSignal, ownedPidfdSignal
	defer func() { ownedWaitid, ownedGroupSignal, ownedPidfdSignal = oldWait, oldGroup, oldSignal }()
	groupCalls := 0
	ownedGroupSignal = func(int, syscall.Signal) error { groupCalls++; return nil }
	ownedWaitid = func(kind, pid int, _ *unix.Siginfo, _ int, _ *unix.Rusage) error {
		if kind != unix.P_PIDFD || pid != fd {
			t.Error("wait used a recovered PID")
		}
		return unix.EIO
	}
	if err := c.Wait(); !errors.Is(err, unix.EIO) {
		t.Fatal("lost actual wait failure", err)
	}
	if groupCalls != 0 || c.PhysicalExitV1() || c.pidfd != fd || c.reaped {
		t.Fatal("wait failure minted closure or used a group number")
	}
	if err := unix.PidfdSendSignal(fd, 0, nil, 0); err != nil {
		t.Fatal("live native handle was lost", err)
	}
	ownedPidfdSignal = func(got int, _ unix.Signal, _ *unix.Siginfo, _ int) error {
		if got != fd {
			t.Error("cleanup changed native identity")
		}
		return unix.EPERM
	}
	if err := c.Signal(os.Kill); !errors.Is(err, unix.EPERM) {
		t.Fatal("owned signal failure lost its cause", err)
	}
	if groupCalls != 0 || c.PhysicalExitV1() {
		t.Fatal("failed pidfd signal fell back to a group or became exit evidence")
	}
	ownedWaitid, ownedPidfdSignal = oldWait, oldSignal
	if err := c.Signal(os.Kill); err != nil {
		t.Fatal(err)
	}
	if groupCalls != 0 {
		t.Fatal("uncertain cleanup fell back to a saved group")
	}
	if err := waitOwnedPIDFD(fd, -1); err != nil {
		t.Fatal(err)
	}
	if !c.PhysicalExitV1() || c.pidfd != -1 {
		t.Fatal("positive same-owner retry did not finish cleanup")
	}
	if !errors.Is(c.lifecycleErr, unix.EIO) || !errors.Is(c.lifecycleErr, unix.EPERM) {
		t.Fatal("later physical closure erased earlier failures", c.lifecycleErr)
	}
}

func TestPhysicalExitRejectsPollErrorsAndUnrelatedReadiness(t *testing.T) {
	old := ownedPoll
	defer func() { ownedPoll = old }()
	for _, tc := range []struct {
		name   string
		n      int
		events int16
		err    error
	}{
		{"empty", 0, 0, nil}, {"HUP", 1, unix.POLLHUP, nil}, {"NVAL", 1, unix.POLLNVAL, nil}, {"ERRIN", 1, unix.POLLERR | unix.POLLIN, nil}, {"otherIN", 1, unix.POLLOUT | unix.POLLIN, nil}, {"error", -1, 0, unix.EIO},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ownedPoll = func(p []unix.PollFd, _ int) (int, error) { p[0].Revents = tc.events; return tc.n, tc.err }
			c := &Cmd{owned: true, pidfd: 17, phase: phaseUncertain}
			if c.PhysicalExitV1() || c.pidfd != 17 || c.reaped {
				t.Fatal("unproved init was released")
			}
		})
	}
}

func TestOwnedExitPollRetriesInterruptedAndEmptyWaits(t *testing.T) {
	old := ownedPoll
	defer func() { ownedPoll = old }()
	calls := 0
	ownedPoll = func(p []unix.PollFd, _ int) (int, error) {
		calls++
		if calls == 1 {
			return -1, unix.EINTR
		}
		if calls == 2 {
			return 0, nil
		}
		p[0].Revents = unix.POLLIN
		return 1, nil
	}
	if err := waitOwnedPIDFD(17, -1); err != nil || calls != 3 {
		t.Fatal("exit wait did not retry same native handle", calls, err)
	}
}

func TestLogicalExitStatusAndDoneDoNotMintPhysicalClosure(t *testing.T) {
	c := &Cmd{done: make(chan struct{})}
	c.finish(&ExitError{Code: 0}, func() {})
	if c.ExitCode() != 0 {
		t.Fatal("logical fixture did not publish its claim")
	}
	if c.PhysicalExitV1() {
		t.Fatal("logical exit status or done channel minted physical evidence")
	}
}

func TestStartCleanupPollFailureRetainsOwnedHandle(t *testing.T) {
	oldGate, oldPoll := afterSetup, ownedPoll
	defer func() { afterSetup, ownedPoll = oldGate, oldPoll }()
	afterSetup = func(c *Cmd) { _ = c.Signal(syscall.SIGTERM) }
	ownedPoll = func(p []unix.PollFd, _ int) (int, error) { p[0].Revents = unix.POLLNVAL; return 1, nil }
	c := lifecycleCmd(t)
	if err := c.Start(); err == nil {
		t.Fatal("cancelled setup succeeded")
	}
	if c.pidfd < 0 || c.PhysicalExitV1() || c.reaped {
		t.Fatal("failed startup cleanup discarded uncertain native ownership")
	}
	fd := c.pidfd
	ownedPoll = oldPoll
	if err := waitOwnedPIDFD(fd, -1); err != nil {
		t.Fatal(err)
	}
	if !c.PhysicalExitV1() || c.pidfd != -1 {
		t.Fatal("same-owner positive retry failed")
	}
}

func TestPhysicalRecoveryDoesNotWaitForStatusEOF(t *testing.T) {
	c := lifecycleCmd(t)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	fd := c.pidfd
	old := ownedWaitid
	ownedWaitid = func(int, int, *unix.Siginfo, int, *unix.Rusage) error { return unix.EIO }
	err := c.Wait()
	ownedWaitid = old
	if !errors.Is(err, unix.EIO) {
		t.Fatal(err)
	}
	if err := waitOwnedPIDFD(fd, -1); err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	_ = c.status.Close()
	c.status = reader
	// The metadata writer remains open in a different owner. A logical
	// status/EOF cannot control recovery of the physically exited process.
	done := make(chan bool, 1)
	go func() { done <- c.PhysicalExitV1() }()
	select {
	case physical := <-done:
		if !physical || c.pidfd != -1 || !errors.Is(c.lifecycleErr, unix.EIO) {
			t.Fatal("physical retry lost proof or original failure")
		}
	case <-time.After(2 * time.Second):
		writer.Close()
		<-done
		t.Fatal("physical recovery waited for unrelated status EOF")
	}
}

func TestGroupSignalNeedsCurrentCreationFdChildOwnership(t *testing.T) {
	c := lifecycleCmd(t)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	oldWait, oldGroup := ownedWaitid, ownedGroupSignal
	defer func() { ownedWaitid, ownedGroupSignal = oldWait, oldGroup }()
	groupCalls := 0
	ownedGroupSignal = func(int, syscall.Signal) error { groupCalls++; return nil }
	ownedWaitid = func(int, int, *unix.Siginfo, int, *unix.Rusage) error { return unix.ECHILD }
	if err := c.Signal(os.Kill); !errors.Is(err, unix.ECHILD) {
		t.Fatal("signal hid lost native child ownership", err)
	}
	if groupCalls != 0 || !c.groupUnsafe {
		t.Fatal("lost child ownership permitted numeric group signal")
	}
	ownedWaitid = oldWait
	if err := c.Wait(); !errors.Is(err, unix.ECHILD) {
		t.Fatal("actual completion erased ownership failure", err)
	}
	if !c.PhysicalExitV1() || groupCalls != 0 {
		t.Fatal("actual same-fd closure reused a group number")
	}
}
