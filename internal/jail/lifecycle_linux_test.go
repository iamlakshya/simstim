package jail

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// lifecycleCmd exercises the public lifecycle independently of kernel
// confinement support. Its owned session leader consumes the set-up pipe.
func lifecycleCmd(t *testing.T) *Cmd {
	t.Helper()
	cmd := exec.CommandContext(context.WithoutCancel(t.Context()), "/bin/sh", "-c", "cat <&3 >/dev/null; exit 7")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return &Cmd{ctx: t.Context(), cmd: cmd, setup: []byte("{}\n"),
		abort: make(chan struct{}), done: make(chan struct{})}
}

func TestPidAndExitCodeDuringStartAndWait(t *testing.T) {
	for range 20 {
		c := lifecycleCmd(t)
		stop, stopped := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(stopped)
			for {
				select {
				case <-stop:
					return
				default:
					_ = c.Pid()
					_ = c.ExitCode()
					runtime.Gosched()
				}
			}
		}()
		if err := c.Start(); err != nil {
			close(stop)
			<-stopped
			t.Fatal(err)
		}
		if c.Pid() != c.cmd.Process.Pid || c.Pid() <= 0 || c.ExitCode() != -1 {
			t.Errorf("after Start: Pid=%d, real=%d, ExitCode=%d", c.Pid(), c.cmd.Process.Pid, c.ExitCode())
		}
		exitSeen := make(chan int, 1)
		go func() {
			<-c.done
			exitSeen <- c.ExitCode()
		}()
		waited := make(chan error, 1)
		go func() { waited <- c.Wait() }()
		if got := <-exitSeen; got != 7 {
			t.Errorf("at done: ExitCode=%d, want 7", got)
		}
		var ended *ExitError
		if err := <-waited; !errors.As(err, &ended) || ended.Code != 7 {
			t.Errorf("Wait = %v, want command exit 7", err)
		}
		close(stop)
		<-stopped
		if c.ExitCode() != 7 {
			t.Errorf("after Wait: ExitCode=%d, want 7", c.ExitCode())
		}
	}
}

func TestExitIsPublishedBeforePipesAndDone(t *testing.T) {
	c := &Cmd{done: make(chan struct{})}
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = read.Close() }()
	defer func() { _ = write.Close() }()
	c.ends = []*os.File{write}
	if got := c.ExitCode(); got != -1 {
		t.Fatalf("before completion: ExitCode=%d, want -1", got)
	}
	releases := 0
	c.finish(&ExitError{Code: 7}, func() {
		releases++
		if got := c.ExitCode(); got != 7 {
			t.Errorf("before releasing caller pipes: ExitCode=%d, want 7", got)
		}
		select {
		case <-c.done:
			t.Error("done closed before caller pipes were released")
		default:
		}
		c.closeEnds(true)
	})
	if releases != 1 {
		t.Errorf("caller pipes were released %d times, want once", releases)
	}
	select {
	case <-c.done:
	default:
		t.Error("completion did not notify waiters")
	}
	if _, err := write.Write([]byte("x")); !errors.Is(err, os.ErrClosed) {
		t.Errorf("the released caller pipe is still usable: %v", err)
	}
}

func TestCombinedOutputOutOfOrderPreservesStreams(t *testing.T) {
	for _, phase := range []string{"running", "waited", "failed"} {
		t.Run(phase, func(t *testing.T) {
			c := lifecycleCmd(t)
			if phase == "failed" {
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				c.ctx = ctx
			}
			err := c.Start()
			if phase == "failed" && err == nil || phase != "failed" && err != nil {
				t.Fatalf("Start = %v in %s", err, phase)
			}
			if phase == "waited" {
				_ = c.Wait()
			}
			if out, err := c.CombinedOutput(); out != nil || err == nil || hintOf(err) == "" {
				t.Errorf("CombinedOutput in %s = %q, %v, want an error with a hint", phase, out, err)
			}
			if c.Stdout != nil || c.Stderr != nil {
				t.Errorf("CombinedOutput in %s changed Stdout or Stderr", phase)
			}
			if phase == "running" {
				_ = c.Wait()
			}
		})
	}
}

func TestRemovalSetupFailureReapsTheStartedStage(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "printf before >&2; printf 'ready\\n'; exec sleep 600")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = read.Close() }()
	cmd.Stdout = write
	if err := cmd.Start(); err != nil {
		_ = write.Close()
		t.Fatal(err)
	}
	_ = write.Close()
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	ready := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(read).ReadString('\n')
		if err == nil && line != "ready\n" {
			err = errors.New("the child did not send its ready marker")
		}
		ready <- err
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = r.Close(), w.Close() // the started stage cannot receive its set-up
	done := make(chan error, 1)
	go func() { done <- writeChildSetup(cmd, w, &setup{Remove: "/unused", SyncFD: 4}) }()
	select {
	case err := <-done:
		if err == nil || hintOf(err) == "" {
			t.Errorf("set-up write = %v, want an error with a hint", err)
		}
	case <-time.After(2 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatal("set-up failure did not kill and join its child")
	}
	if cmd.ProcessState == nil {
		t.Fatal("set-up failure left an unreaped child and stderr copier")
	}
	if got := stderr.String(); got != "before" {
		t.Errorf("joined stderr = %q, want before", got)
	}
	if _, err := io.ReadAll(read); err != nil {
		t.Fatal(err)
	}
}
