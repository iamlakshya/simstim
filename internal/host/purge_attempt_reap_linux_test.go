//go:build linux

package host

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// This fixture creates only an actual local child and its original wait
// reader. It neither calls a manager nor constructs scope/session authority.
func actualPurgeReapFixtureV3(t *testing.T) (*purgeAttemptExecV1, *purgeAttemptReapV3) {
	t.Helper()
	command := exec.Command("/bin/sh", "-c", "exit 0")
	owner := &purgeAttemptExecV1{command: command, creationFD: -1}
	hold, err := newPurgeAttemptReapV3(owner, command)
	if err != nil {
		t.Fatal(err)
	}
	owner.reap = hold
	command.SysProcAttr = &syscall.SysProcAttr{PidFD: &owner.creationFD}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	owner.process = command.Process
	if owner.creationFD < 0 {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatal("the actual child has no atomic pidfd")
	}
	owner.creation = os.NewFile(uintptr(owner.creationFD), "actual-test-original-creation")
	if err := hold.beginOriginalWaitV3(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		hold.mu.Lock()
		if !hold.released {
			hold.released = true
			close(hold.allow)
		}
		hold.mu.Unlock()
		<-owner.waitDone
		_ = owner.creation.Close()
	})
	polls := []unix.PollFd{{Fd: int32(owner.creation.Fd()), Events: unix.POLLIN}}
	if n, err := unix.Poll(polls, 5000); err != nil || n != 1 || polls[0].Revents&unix.POLLIN == 0 {
		t.Fatalf("actual original child exit: %d %v", n, err)
	}
	return owner, hold
}

func TestPurgeReapGateRetainsActualEarlyExitAcrossCancellationV3(t *testing.T) {
	owner, hold := actualPurgeReapFixtureV3(t)
	if err := hold.checkHeldV3(t.Context()); err != nil {
		t.Fatal("early native exit lost its unreaped reservation", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := hold.checkHeldV3(ctx); err == nil {
		t.Fatal("cancelled request reported a valid current check")
	}
	select {
	case <-owner.waitDone:
		t.Fatal("request cancellation reaped the child during adoption")
	default:
	}
	if owner.command.ProcessState != nil {
		t.Fatal("the original Cmd.Wait ran before settlement")
	}
	for _, scope := range []*purgeAttemptScopeV3{nil, {owner: owner}, {owner: owner, adopted: true, notified: true}} {
		if err := hold.releaseAfterScopeV3(scope); err == nil {
			t.Fatal("phase-shaped scope data released the original reaping hold")
		}
	}
	if err := hold.checkHeldV3(t.Context()); err != nil {
		t.Fatal("refused metadata changed the real reaping gate", err)
	}
}

func TestPurgeReapGateRefusesPrematureNativeWaitMutantV3(t *testing.T) {
	owner, hold := actualPurgeReapFixtureV3(t)
	// Model the forbidden immediate-Wait mutation using the SAME actual Cmd.
	if err := owner.command.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := hold.checkHeldV3(t.Context()); err == nil {
		t.Fatal("an already-reaped PID was admitted for numeric manager migration")
	}
	if err := checkPurgeOriginalPidfdV3(owner); err == nil {
		t.Fatal("a retained exited pidfd was treated as an unreaped PID reservation")
	}
}

func TestPurgeReapGateRefusesAnotherActualChildPidfdV3(t *testing.T) {
	owner, hold := actualPurgeReapFixtureV3(t)
	other, _ := actualPurgeReapFixtureV3(t)
	original, descriptor := owner.creation, owner.creationFD
	owner.creation, owner.creationFD = other.creation, other.creationFD
	if err := hold.checkHeldV3(t.Context()); err == nil {
		t.Fatal("another real unreaped child replaced the original atomic creation")
	}
	owner.creation, owner.creationFD = original, descriptor
	if err := hold.checkHeldV3(t.Context()); err != nil {
		t.Fatal("refused foreign handle changed the original reservation", err)
	}
}

func TestPurgeAdoptionLatchDelaysEOFAndDoesNotConsumeCompletionV3(t *testing.T) {
	fd, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(fd), "actual-test-adoption-latch")
	defer file.Close()
	done := make(chan error, 1)
	go func() { done <- waitPurgeAttemptAdoptionV3(file) }()
	select {
	case err := <-done:
		t.Fatal("unset adoption returned before actual manager settlement", err)
	case <-time.After(20 * time.Millisecond):
	}
	var value [8]byte
	binary.NativeEndian.PutUint64(value[:], 1)
	if _, err := file.Write(value[:]); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("actual settled adoption did not release bootstrap")
	}
	if err := ordinaryJoinLatchV1(file, true); err != nil {
		t.Fatal("waiting consumed the original completion observation", err)
	}
	if err := waitPurgeAttemptAdoptionV3(file); err != nil {
		t.Fatal("same actual completion could not be reobserved", err)
	}
}

func TestPurgeScopeClientHalfCloseCannotOpenReapingGateV3(t *testing.T) {
	owner, hold := actualPurgeReapFixtureV3(t)
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	var connections [2]*net.UnixConn
	for index, fd := range fds {
		file := os.NewFile(uintptr(fd), "actual-test-request-transport")
		connection, err := net.FileConn(file)
		_ = file.Close()
		if err != nil {
			t.Fatal(err)
		}
		connections[index] = connection.(*net.UnixConn)
		defer connections[index].Close()
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); _, _ = io.Copy(io.Discard, connections[0]); cancel() }()
	if err := connections[1].CloseWrite(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("actual request half-close did not cancel its reader")
	}
	if ctx.Err() == nil {
		t.Fatal("actual disconnected request remained live")
	}
	if err := hold.checkHeldV3(t.Context()); err != nil {
		t.Fatal("client EOF released the original unreaped child", err)
	}
	select {
	case <-owner.waitDone:
		t.Fatal("client EOF let Cmd.Wait reap during adoption")
	default:
	}
}

func TestPurgeScopeRetryCannotRedispatchOriginalCreationV3(t *testing.T) {
	owner, hold := actualPurgeReapFixtureV3(t)
	owner.start = &purgeAttemptStartV2{creator: owner, command: owner.command}
	scope := &purgeAttemptScopeV3{owner: owner, dispatched: true}
	owner.scope, owner.start.scope = scope, scope
	if err := scope.adoptOriginalV3(); err == nil {
		t.Fatal("same original attempt dispatched another scope transaction")
	}
	if scope.call != nil || scope.reply != nil || scope.job != nil {
		t.Fatal("refused retry created another manager call")
	}
	if err := hold.checkHeldV3(t.Context()); err != nil {
		t.Fatal("refused retry released the actual unreaped child", err)
	}
}
