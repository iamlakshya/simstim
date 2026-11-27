package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mainArgsEnv holds the command line, split by mainArgsSep, that
// TestMainInAChild runs. mainSceneEnv names what the child sets up first.
const (
	mainArgsEnv  = "CLI_TEST_MAIN_ARGS"
	mainSceneEnv = "CLI_TEST_MAIN_SCENE"
	mainArgsSep  = "\x1f"
	// childFailed is the exit code of a child whose scene failed in its
	// setup or its cleanup: no code of Main's.
	childFailed = 97
)

// TestMainInAChild runs Main in a child. Scene cleanups finish before the
// child exits. Native host scenes are registered by the Unix fixture file.
var mainChildScenes = map[string]func(*testing.T) func(){}

func TestMainInAChild(t *testing.T) {
	args := os.Getenv(mainArgsEnv)
	if args == "" {
		return
	}
	scene := os.Getenv(mainSceneEnv)
	if scene == "completion-signal" {
		completionSignalScene()
	}
	code := 0
	ok := t.Run("main", func(t *testing.T) {
		if scene == "cleanup-fails" {
			t.Cleanup(func() { t.Error("the scene's cleanup failed") })
		}
		var after func()
		if scene != "" && scene != "cleanup-fails" && scene != "completion-signal" {
			prepare, exists := mainChildScenes[scene]
			if !exists {
				t.Fatalf("unknown child scene %q", scene)
			}
			after = prepare(t)
		}

		os.Args = append([]string{"client"}, strings.Split(args, mainArgsSep)...)
		code = Main()
		if after != nil {
			after()
		}
	})
	if ok {
		os.Exit(code)
	}
}

// A child whose scene fails in its cleanup says so with its exit code, not
// with Main's, which the tests would take for a clean run, and prints what
// failed.
func TestAChildWhoseCleanupFailsSaysSo(t *testing.T) {
	newTestHome(t)
	cmd, out := childWithoutTerminal(t, "cleanup-fails", "version")
	if code := exitOf(t, cmd); code != childFailed || !strings.Contains(out.String(), "the scene's cleanup failed") {
		t.Errorf("a child whose cleanup failed exited with %d, want %d, and said:\n%s", code, childFailed, out.String())
	}
}

// childProc wraps a started child process with a single Wait goroutine so
// exitOf and t.Cleanup never call Wait or read ProcessState concurrently.
type childProc struct {
	*exec.Cmd
	done chan struct{}
	code int
}

func startChild(t *testing.T, cmd *exec.Cmd) *childProc {
	t.Helper()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c := &childProc{Cmd: cmd, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		if cmd.ProcessState != nil {
			c.code = cmd.ProcessState.ExitCode()
		}
		close(c.done)
	}()
	t.Cleanup(func() {
		select {
		case <-c.done:
		default:
			_ = cmd.Process.Kill()
			<-c.done
		}
	})
	return c
}

// child starts the test binary as a client that runs args. With term, the
// terminal is its stdin, stdout and stderr. Without it, the buffer gets
// stderr and stdout.
func childCommand(scene string, args ...string) (*exec.Cmd, *bytes.Buffer) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestMainInAChild$")
	cmd.Env = append(os.Environ(), mainArgsEnv+"="+strings.Join(args, mainArgsSep), mainSceneEnv+"="+scene, "NO_COLOR=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stderr
	return cmd, &stderr
}
func childWithoutTerminal(t *testing.T, scene string, args ...string) (*childProc, *bytes.Buffer) {
	t.Helper()
	cmd, stderr := childCommand(scene, args...)
	return startChild(t, cmd), stderr
}

// childWait is how long a test waits for a child. A child takes about a
// second, but a busy machine can make it far slower.
const childWait = 60 * time.Second

func waitChild(c *childProc, d time.Duration) (int, bool) {
	select {
	case <-c.done:
		return c.code, true
	case <-time.After(d):
		_ = c.Process.Kill()
		<-c.done
		return 0, false
	}
}
func exitOf(t *testing.T, c *childProc) int {
	t.Helper()
	code, ok := waitChild(c, childWait)
	if !ok {
		t.Fatal("the client did not end after the signal")
	}
	return code
}

// A timeout in waitChild kills the child and waits for cmd.Wait and its output
// copy goroutines to finish, so reading stderr immediately never races and the
// subtest's cleanup never calls Wait a second time.
func TestChildTimeoutCleanupJoinsSingleWaiter(t *testing.T) {
	var proc *childProc
	var stderr bytes.Buffer
	started := filepath.Join(t.TempDir(), "started")
	t.Run("timed-out-child", func(t *testing.T) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestNoisyChild$")
		cmd.Env = append(os.Environ(), "CLI_TEST_NOISY_CHILD="+started)
		cmd.Stderr = &stderr
		cmd.Stdout = &stderr
		proc = startChild(t, cmd)
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
			if _, err := os.Stat(started); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("noisy child never started")
			}
		}
		if _, ok := waitChild(proc, 10*time.Millisecond); ok {
			t.Fatal("noisy loop finished within 10ms")
		}
		if stderr.String() == "" {
			t.Log("child exited before flushing noise")
		}
	})
	select {
	case <-proc.done:
	default:
		t.Fatal("cleanup returned before the child waiter finished")
	}
}

// A signal ends the command's context before it closes interrupted, so a
// command that stops at interrupted always finds its context ended, and
// Main exits with 130, not 1.
func TestASignalEndsTheContextFirst(t *testing.T) {
	withSignal(t)
	ended := false
	endAtSignal(func() {
		ended = true
		if signalled() {
			t.Error("interrupted closed before the context ended")
		}
	})
	if !ended || !signalled() {
		t.Errorf("the context ended: %v, interrupted closed: %v", ended, signalled())
	}
}

// TestNoisyChild is the owned subprocess of the timeout test. Running the
// test binary itself keeps the waiter and output-copy checks portable.
func TestNoisyChild(t *testing.T) {
	path := os.Getenv("CLI_TEST_NOISY_CHILD")
	if path == "" {
		return
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for {
		fmt.Fprintln(os.Stderr, "noise")
	}
}
