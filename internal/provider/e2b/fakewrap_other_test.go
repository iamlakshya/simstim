//go:build !windows

package e2b

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/client"
	"golang.org/x/sys/unix"
)

// drainPipe takes only bytes ready now. Its cap also ends a drain when a
// descendant keeps writing, so SendSignal never waits for that writer's EOF.
func drainPipe(f *os.File, dst *[]byte) {
	const maxDrainedOutput = 1 << 20
	rc, err := f.SyscallConn()
	if err != nil {
		return
	}
	_ = rc.Control(func(fd uintptr) {
		_ = syscall.SetNonblock(int(fd), true)
		buf := make([]byte, 32<<10)
		for len(*dst) < maxDrainedOutput {
			n, err := syscall.Read(int(fd), buf[:min(len(buf), maxDrainedOutput-len(*dst))])
			if n > 0 {
				*dst = append(*dst, buf[:n]...)
			}
			if err == syscall.EINTR {
				continue
			}
			if n == 0 || err != nil {
				break
			}
		}
	})
}

// leadGroup makes the wrapper the leader of a process group of its own, and
// returns the group's number.
func leadGroup() (int, error) {
	if os.Getenv(failLeadGroupEnv) != "" {
		return 0, errors.New("injected setpgid failure")
	}
	if err := syscall.Setpgid(0, 0); err != nil {
		return 0, err
	}
	if pg := syscall.Getpgrp(); pg != os.Getpid() {
		return 0, fmt.Errorf("process group is %d, want %d", pg, os.Getpid())
	}
	return os.Getpid(), nil
}

// keepFromCommand marks the wrapper's own files close-on-exec, so the command
// and its children do not get them.
func keepFromCommand(fds ...int) {
	for _, fd := range fds {
		syscall.CloseOnExec(fd)
	}
}

// useTestStderr makes the test's stderr, file fd, the wrapper's own.
func useTestStderr(fd int) {
	if unix.Dup2(fd, 2) == nil {
		_ = unix.Close(fd)
	}
}

// endGroup kills the wrapper's group: the command, and every child that the
// command left in it. The wrapper first moves to its parent's group, so that
// it lives to kill again: a child that a fork was making during one kill dies
// in the next. The group's number is the wrapper's own, so while the wrapper
// lives no other group can have it. Without a group, the wrapper kills only
// the command.
func endGroup(cmd *exec.Cmd, group int) {
	_ = cmd.Process.Kill()
	if group == 0 {
		return
	}
	if pg, err := syscall.Getpgid(os.Getppid()); err != nil || syscall.Setpgid(0, pg) != nil {
		_ = syscall.Kill(0, syscall.SIGKILL) // one kill, of the wrapper too
		return
	}
	for range 400 {
		if syscall.Kill(-group, syscall.SIGKILL) == syscall.ESRCH {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A command that ends well, and leaves a child with its streams redirected:
// the stream ends with the command, and the shutdown still ends the child.
// The child holds the write end of a FIFO, so the test sees its end as the
// FIFO's EOF: the test needs no process number.
func TestFakeEnvdEndsTheChildOfAnEndedCommand(t *testing.T) {
	e, envd := envdFor(t, false)
	fifo := filepath.Join(t.TempDir(), "alive")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	// The read end opens first, without waiting, so the shell's open of the
	// write end does not wait either.
	r, err := os.OpenFile(fifo, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	// Reads wait: Go does not poll a FIFO on macOS (go.dev/issue/24164).
	if err := syscall.SetNonblock(int(r.Fd()), false); err != nil {
		t.Fatal(err)
	}
	// The shell opens the write end before it starts the child, which gets
	// it from the shell. Then the shell lets go of its own.
	out, err := e.Run(context.Background(), fmt.Sprintf("exec 3>%q; sleep 120 >/dev/null 2>&1 </dev/null & exec 3>&-; echo ok", fifo), nil)
	if err != nil || string(out) != "ok\n" {
		t.Fatalf("run = %q, %v", out, err)
	}
	// The shell has ended, so only the child holds the FIFO now.
	ended := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, r)
		close(ended)
	}()
	select {
	case <-ended:
		t.Fatal("the child ended before the shutdown")
	case <-time.After(100 * time.Millisecond):
	}
	if err := envd.shutdown(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ended:
	case <-time.After(30 * time.Second):
		t.Fatal("the child outlived the shutdown by 30s")
	}
}

// The wrapper stays alive, and leads its group, after its command ended
// well. A shutdown that comes later then ends that group while its leader
// lives, so its number cannot have passed to another group. Nothing outside
// the group is signalled. The test asks about the wrapper and the other
// process through their os.Process, which the test owns.
func TestFakeEnvdKeepsTheWrapperUntilTheShutdown(t *testing.T) {
	e, envd := envdFor(t, false)
	outside := exec.Command("sleep", "120")
	outside.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := outside.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = outside.Process.Kill(); _ = outside.Wait() }()
	if out, err := e.Run(context.Background(), "echo ok", nil); err != nil || string(out) != "ok\n" {
		t.Fatalf("run = %q, %v", out, err)
	}
	envd.mu.Lock()
	var cmds []*fakeCmd
	for _, c := range envd.procs {
		cmds = append(cmds, c)
	}
	envd.mu.Unlock()
	if len(cmds) != 1 {
		t.Fatalf("%d commands, want 1", len(cmds))
	}
	c := cmds[0]
	// Signal 0 sends nothing. os.Process gives ErrProcessDone once the
	// fake's Wait has reaped the wrapper.
	if err := c.wrapper.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("the wrapper ended with its command: %v", err)
	}
	w := c.wrapper.Process.Pid // not reaped, so still the wrapper's
	if g, err := syscall.Getpgid(w); err != nil || g != w {
		t.Fatalf("the wrapper %d is in group %d (%v), want its own", w, g, err)
	}
	if err := envd.shutdown(); err != nil {
		t.Fatal(err)
	}
	if c.wrapper.ProcessState == nil {
		t.Fatalf("the wrapper %d outlived the shutdown", w)
	}
	if err := outside.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("the shutdown ended a process outside the wrapper's group: %v", err)
	}
}

// When leadGroup fails on Unix, the wrapper writes the error to stderr and
// exits non-zero before starting the command, and the shutdown reports the
// wrapper failure.
func TestFakeEnvdWrapperFailsWhenLeadGroupFails(t *testing.T) {
	e, envd := envdFor(t, false)
	envd.mu.Lock()
	envd.failLeadGroup = true
	envd.mu.Unlock()
	ran := filepath.Join(t.TempDir(), "ran")
	out, err := e.Run(context.Background(), fmt.Sprintf("echo ran > %q", ran), nil)
	if err == nil || !strings.Contains(err.Error(), "lead process group: injected setpgid failure") {
		t.Fatalf("Run = %q, %v, want the process-group error in stderr", out, err)
	}
	if _, statErr := os.Stat(ran); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("stat %s = %v, want the command not to have run", ran, statErr)
	}
	serr := envd.shutdown()
	if serr == nil || !strings.Contains(serr.Error(), "a wrapper of the fake envd failed") {
		t.Fatalf("shutdown = %v, want it to report the wrapper failure", serr)
	}
}

// escapedRun is a command whose child left the wrapper's group while holding
// the command's output open.
type escapedRun struct {
	envd       *fakeEnvd
	proc       client.Proc
	ready      func() bool
	atHoldGate func() bool
	openHold   func()
	release    func()
	gone       func() bool
	reap       func(bool)
}

func startEscaped(t *testing.T, drop bool) *escapedRun {
	t.Helper()
	esc := startEscapedOpt(t, drop, false)
	waitFor(t, "the escaped child's start", esc.ready)
	return esc
}

// startEscapedOpt starts a command whose perl child leaves the wrapper's group
// and holds the command's stdout and stderr open until release closes hold.
// When gateHold is set, the child pauses right before opening hold until
// openHold runs, so a test can prove that readiness on alive never fires
// before hold is open. On Linux, ownEscaped keeps the orphaned grandchild as
// our own child so reap waits for it on every exit path. On macOS, which has
// no subreaper, launchd reaps it and gone proves its end through alive's EOF.
func startEscapedOpt(t *testing.T, drop, gateHold bool) *escapedRun {
	t.Helper()
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("no perl to leave the process group with")
	}
	reap := ownEscaped(t)
	e, envd := envdFor(t, false)
	envd.wait = 300 * time.Millisecond
	if drop {
		envd.mu.Lock()
		envd.drop = true
		envd.mu.Unlock()
	}
	dir := t.TempDir()
	hold, alive := filepath.Join(dir, "hold"), filepath.Join(dir, "alive")
	var gateReady, gateWait string
	if gateHold {
		gateReady = filepath.Join(dir, "gate-ready")
		gateWait = filepath.Join(dir, "gate-wait")
	}
	held, watch, gateReadyR, gateWaitW := -1, -1, -1, -1
	var started, gated atomic.Bool
	openHold := func() {
		if gateWaitW >= 0 {
			_ = syscall.Close(gateWaitW)
			gateWaitW = -1
		}
		if gateReadyR >= 0 {
			_ = syscall.Close(gateReadyR)
			gateReadyR = -1
		}
	}
	release := func() {
		if held >= 0 {
			_ = syscall.Close(held) // the child reads EOF and ends
			held = -1
		}
	}
	ready := func() bool {
		if started.Load() {
			return true
		}
		var b [1]byte
		n, _ := syscall.Read(watch, b[:])
		if n == 1 {
			started.Store(true)
			return true
		}
		return false
	}
	atHoldGate := func() bool {
		if gated.Load() {
			return true
		}
		if gateReadyR < 0 {
			return false
		}
		var b [1]byte
		n, _ := syscall.Read(gateReadyR, b[:])
		if n == 1 {
			gated.Store(true)
			return true
		}
		return false
	}
	// gone says that no process holds alive open: read gives 0 and no
	// error. It gives EAGAIN while the child lives.
	gone := func() bool {
		var b [1]byte
		n, err := syscall.Read(watch, b[:])
		return n == 0 && err == nil
	}
	t.Cleanup(func() {
		openHold()
		release()
		if (started.Load() || gated.Load()) && watch >= 0 {
			for deadline := time.Now().Add(30 * time.Second); !gone(); time.Sleep(5 * time.Millisecond) {
				// Wake a broken helper if a mutant blocked in open(hold).
				if w, err := syscall.Open(hold, syscall.O_WRONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0); err == nil {
					_ = syscall.Close(w)
				}
				if time.Now().After(deadline) {
					t.Error("the escaped child outlived its FIFO by 30s")
					break
				}
			}
		}
		if watch >= 0 {
			_ = syscall.Close(watch)
			watch = -1
		}
		_ = envd.shutdown()
		select {
		case <-envd.done:
		case <-time.After(30 * time.Second):
		}
		reap(false)
	})
	fifos := []string{hold, alive}
	if gateHold {
		fifos = append(fifos, gateReady, gateWait)
	}
	for _, f := range fifos {
		if err := syscall.Mkfifo(f, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r, err := syscall.Open(hold, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	held, err = syscall.Open(hold, syscall.O_WRONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	_ = syscall.Close(r)
	if err != nil {
		t.Fatal(err)
	}
	if watch, err = syscall.Open(alive, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0); err != nil {
		t.Fatal(err)
	}
	if gateHold {
		if gateReadyR, err = syscall.Open(gateReady, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0); err != nil {
			t.Fatal(err)
		}
		gr, err := syscall.Open(gateWait, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		gateWaitW, err = syscall.Open(gateWait, syscall.O_WRONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
		_ = syscall.Close(gr)
		if err != nil {
			t.Fatal(err)
		}
	}
	const helper = `my $pp = $$; my $p = fork; die unless defined $p; exit 0 if $p; ` +
		`select(undef, undef, undef, 0.001) while getppid() == $pp; ` +
		`defined(setpgrp(0, 0)) or die; ` +
		`if ($ARGV[2] ne "") { ` +
		`sysopen(GR, $ARGV[2], O_WRONLY | O_NONBLOCK) or exit 0; ` +
		`syswrite(GR, "g") or exit 0; close(GR); ` +
		`sysopen(GW, $ARGV[3], O_RDONLY | O_NONBLOCK) or die; ` +
		`defined(fcntl(GW, F_SETFL, 0)) or die; ` +
		`sysread(GW, $_, 1); close(GW); ` +
		`} ` +
		`sysopen(H, $ARGV[0], O_RDONLY | O_NONBLOCK) or die; ` +
		`defined(fcntl(H, F_SETFL, 0)) or die; ` +
		`sysopen(A, $ARGV[1], O_WRONLY | O_NONBLOCK) or exit 0; ` +
		`syswrite(A, "r") or exit 0; ` +
		`sysread(H, $_, 1)`
	p, err := e.Start(context.Background(), fmt.Sprintf("exec perl -MFcntl -e '%s' %q %q %q %q", helper, hold, alive, gateReady, gateWait))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Kill)
	return &escapedRun{
		envd:       envd,
		proc:       p,
		ready:      ready,
		atHoldGate: atHoldGate,
		openHold:   openHold,
		release:    release,
		gone:       gone,
		reap:       reap,
	}
}

// A child that leaves its group escapes the shutdown. The shutdown then fails
// the test in its time, and names what still runs. No timing decides the
// test, and it kills no process by its number. Both FIFOs are open before the
// child signals readiness, cleanup releases and waits on every exit path, and
// the shutdown runs behind an independent deadline of the test's own.
func TestFakeEnvdShutdownNamesWhatDidNotEnd(t *testing.T) {
	t.Run("names what still runs", func(t *testing.T) {
		esc := startEscaped(t, false)
		go func() { _, _ = io.Copy(io.Discard, esc.proc.Stdout()) }()
		err := shutdownWithin(t, esc.envd, 30*time.Second)
		if err == nil || !strings.Contains(err.Error(), "did not shut down in 300ms") || !strings.Contains(err.Error(), "the stream of the command") {
			t.Fatalf("shutdown = %v, want it to name the stream that did not end", err)
		}
		esc.release()
		waitFor(t, "the end of the escaped child", esc.gone)
		waitFor(t, "the end of its stream", func() bool { return len(esc.envd.run.left()) == 0 })
		esc.reap(true)
	})
	t.Run("early release after readiness", func(t *testing.T) {
		esc := startEscapedOpt(t, false, true)
		waitFor(t, "the escaped child reaching the hold gate or signalling readiness", func() bool {
			return esc.ready() || esc.atHoldGate()
		})
		if esc.ready() {
			esc.release()
			esc.openHold()
			t.Fatal("the escaped child signalled readiness on alive before opening hold")
		}
		esc.openHold()
		waitFor(t, "the escaped child's readiness", esc.ready)
		esc.release()
		waitFor(t, "the end of the escaped child", esc.gone)
		if err := shutdownWithin(t, esc.envd, 30*time.Second); err != nil {
			t.Fatalf("shutdown after early release = %v, want nil", err)
		}
		esc.reap(true)
	})
}

// checkDroppedStreamEscapedChild checks that a dropped stream's drainers stay
// in shutdown accounting when an escaped child keeps an output descriptor
// open.
func checkDroppedStreamEscapedChild(t *testing.T) {
	t.Helper()
	esc := startEscaped(t, true)
	_, _ = io.ReadAll(esc.proc.Stdout())
	err := shutdownWithin(t, esc.envd, 30*time.Second)
	if err == nil || !strings.Contains(err.Error(), "did not shut down in 300ms") || !strings.Contains(err.Error(), "the dropped stream of the command") {
		t.Fatalf("shutdown = %v, want it to name the dropped stream that did not end", err)
	}
	esc.release()
	waitFor(t, "the end of the escaped child", esc.gone)
	waitFor(t, "the end of its dropped stream", func() bool { return len(esc.envd.run.left()) == 0 })
	esc.reap(true)
}
