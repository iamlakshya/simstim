package jail

import (
	"bufio"
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestRelayNeverSignalsAfterReaping(t *testing.T) {
	for range 20 {
		process, err := os.StartProcess("/bin/sh", []string{"sh", "-c", "exit 7"}, &os.ProcAttr{
			Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
		})
		if err != nil {
			t.Fatal(err)
		}
		sigs := make(chan os.Signal, 8) // never register handlers in this test process
		var reaped, late, unlocked atomic.Bool
		r := startRelay(process.Pid, sigs, func(int, syscall.Signal) error {
			if reaped.Load() {
				late.Store(true)
			}
			return nil
		})
		r.check = func() {
			if r.mu.TryLock() {
				unlocked.Store(true)
				r.mu.Unlock()
			}
		}
		r.wait4 = func(pid int, status *syscall.WaitStatus, flags int, usage *syscall.Rusage) (int, error) {
			got, err := syscall.Wait4(pid, status, flags, usage)
			if got == process.Pid {
				reaped.Store(true)
			}
			return got, err
		}
		floodDone := make(chan struct{})
		go func() {
			defer close(floodDone)
			for {
				select {
				case <-r.done:
					return
				case sigs <- syscall.SIGTERM:
					runtime.Gosched()
				}
			}
		}()
		status, err := reapWithRelay(process.Pid, r)
		_ = process.Release()
		r.forward(syscall.SIGTERM) // deterministic queued/late callback after reap
		r.stop()
		r.stop()
		<-floodDone
		<-r.finished
		if err != nil || status.ExitStatus() != 7 || !reaped.Load() {
			t.Fatalf("reap = %v, %v, reaped=%v", status, err, reaped.Load())
		}
		if late.Load() || unlocked.Load() {
			t.Errorf("late signal=%v, disarm outside ownership mutex=%v", late.Load(), unlocked.Load())
		}
	}
}

func TestLateSignalsLeaveOrphanCleanupAndStatusIntact(t *testing.T) {
	inMode(t, testMode{"landlock", false})
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	eventsR, eventsW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = eventsR.Close() }()
	gateR, gateW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gateW.Close() }()
	c, err := Command(ctx, Spec{Argv: []string{"/bin/sh", "-c", "sleep 600 & exit 7"}, Binds: libBinds(),
		Files: []*os.File{eventsW, gateR}, Seccomp: ProfileConverter, Env: append(probeEnv, "JAILTEST_AFTER_REAP_GATE=1")})
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := c.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := c.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	_, _ = eventsW.Close(), gateR.Close()
	waited := false
	defer func() {
		if !waited {
			c.killGroup()
			_ = c.Wait()
		}
	}()
	_ = eventsR.SetReadDeadline(time.Now().Add(5 * time.Second))
	if line, err := bufio.NewReader(eventsR).ReadString('\n'); err != nil || line != "reaped\n" {
		t.Fatalf("post-reap gate = %q, %v", line, err)
	}
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT} {
		if err := c.Signal(sig); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = gateW.Write([]byte{1})
	_ = gateW.Close()
	if !readEnds(stdout, 5*time.Second) || !readEnds(stderr, 5*time.Second) {
		t.Fatal("a late signal skipped orphan cleanup before Wait")
	}
	err = c.Wait()
	waited = true
	var ended *ExitError
	if !errors.As(err, &ended) || ended.Code != 7 || ended.Signal != 0 {
		t.Errorf("a late signal changed the main command's end: %v", err)
	}
}

func TestOrphanIsReapedWhileTheMainCommandRuns(t *testing.T) {
	inMode(t, testMode{"landlock", false})
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	eventsR, eventsW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = eventsR.Close() }()
	c, err := Command(ctx, Spec{Argv: []string{"/bin/sh", "-c", "sh -c '(sleep 0.05) &' ; read hold; exit 7"},
		Binds: libBinds(), Files: []*os.File{eventsW}, Seccomp: ProfileConverter,
		Env: append(probeEnv, "JAILTEST_REAP_ORPHAN=1")})
	if err != nil {
		t.Fatal(err)
	}
	input, err := c.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close() }()
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	_ = eventsW.Close()
	waited := false
	defer func() {
		if !waited {
			c.killGroup()
			_ = c.Wait()
		}
	}()
	_ = eventsR.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(eventsR).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "orphan" {
		t.Fatalf("orphan was not reaped before the main command's input gate: %q, %v", line, err)
	}
	_ = input.Close()
	err = c.Wait()
	waited = true
	var ended *ExitError
	if !errors.As(err, &ended) || ended.Code != 7 {
		t.Errorf("main command exit changed: %v", err)
	}
}
