package jail

import (
	"errors"
	"os"
	"os/exec"
	"sync/atomic"
	"syscall"
	"testing"
)

func TestOuterRelayDisarmsBeforeTheOwnedChildIsReaped(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", "exit 7")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var forwarded atomic.Bool
	relay := startRelay(cmd.Process.Pid, make(chan os.Signal), func(int, syscall.Signal) error {
		forwarded.Store(true) // never signal a PID after the test reaps it
		return nil
	})
	defer func() {
		relay.stop()
		<-relay.finished
	}()
	reaps := 0
	err := waitChildWithRelay(cmd.Process.Pid, relay, func() error {
		reaps++
		relay.mu.Lock()
		pid := relay.pid
		relay.mu.Unlock()
		if pid != 0 {
			t.Errorf("the owned child was reaped while forwarding still named pid %d", pid)
		}
		select {
		case <-relay.done:
		default:
			t.Error("reaping began before the relay was disarmed")
		}
		waitErr := cmd.Wait()
		relay.forward(syscall.SIGTERM)
		return waitErr
	}, cmd.Process.Kill)
	if err != nil || reaps != 1 || cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != 7 || forwarded.Load() {
		t.Errorf("outer wait: %v, reaps=%d, state=%v, late forwarding=%v", err, reaps, cmd.ProcessState, forwarded.Load())
	}
}

func TestOuterRelayDoesNotKillAfterOwnershipWasLost(t *testing.T) {
	relay := startRelay(os.Getpid(), make(chan os.Signal), func(int, syscall.Signal) error {
		t.Error("an unowned PID was signalled")
		return nil
	})
	defer func() {
		relay.stop()
		<-relay.finished
	}()
	var reaped, killed bool
	err := waitChildWithRelay(os.Getpid(), relay, func() error {
		reaped = true
		return nil
	}, func() error {
		killed = true
		return nil
	})
	if !errors.Is(err, syscall.ECHILD) || !reaped || killed {
		t.Errorf("lost child ownership: %v, reap cleanup=%v, kill=%v", err, reaped, killed)
	}
}

func TestStartWithSyncClosesTheParentsReadEndOnEveryPath(t *testing.T) {
	for _, fail := range []bool{false, true} {
		read, write, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = read.Close() }()
		defer func() { _ = write.Close() }()
		program, argv := "/bin/sh", []string{"-c", "cat <&3 >/dev/null"}
		if fail {
			program = "/does-not-exist/jail-test-child"
		}
		cmd := exec.CommandContext(t.Context(), program, argv...)
		setupW, startErr := startWithSync(cmd, []*os.File{nil}, 3, read)
		if setupW != nil {
			defer func() { _ = setupW.Close() }()
		}
		if _, err := read.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
			t.Errorf("outer retained its sync read end after start: %v", err)
		}
		_ = write.Close()
		if fail {
			if startErr == nil || setupW != nil {
				t.Errorf("invalid child started: %v", startErr)
			}
			continue
		}
		if startErr != nil {
			t.Fatal(startErr)
		}
		_ = setupW.Close()
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
}
