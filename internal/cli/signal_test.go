package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
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

// TestMainInAChild runs Main in a child process for the signal tests. In
// a normal run it does nothing. The scene "setup-trust" starts a test host
// with a repository whose setup script needs an answer, names its folder
// on stderr, and prints the answers that the client keeps after Main
// returns. The scene "cleanup-fails" has a cleanup that fails. The scene
// runs as a subtest, so that its cleanups, which end the test host and
// remove its folder, run before the child exits: os.Exit runs none. The
// child exits with Main's code when the scene's setup and cleanup went
// well. When they did not, the test returns: the testing package prints
// what failed, and the child exits with childFailed (hosttest.ExitCode).
func TestMainInAChild(t *testing.T) {
	args := os.Getenv(mainArgsEnv)
	if args == "" {
		return
	}
	scene := os.Getenv(mainSceneEnv)
	code := 0
	ok := t.Run("main", func(t *testing.T) {
		if scene == "cleanup-fails" {
			t.Cleanup(func() { t.Error("the scene's cleanup failed") })
		}
		if scene == "setup-trust" {
			th := newTestHost(t)
			fmt.Fprintf(os.Stderr, "test host: %s\n", th.h.Root)
			hook := filepath.Join(th.h.Home, "proj", "."+brand.Name, "setup")
			if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(hook, []byte("#!/bin/sh\n"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		os.Args = append([]string{"client"}, strings.Split(args, mainArgsSep)...)
		code = Main()
		if scene == "setup-trust" {
			fmt.Printf("\r\nkept answers: %v\r\n", keptAnswers("box"))
		}
	})
	if ok {
		os.Exit(code)
	}
}

// A child's failed run exits with childFailed, a code that Main never has.
func init() {
	if os.Getenv(mainArgsEnv) != "" {
		hosttest.ExitCode = func(code int) int {
			if code != 0 {
				return childFailed
			}
			return code
		}
	}
}

// A child whose scene fails in its cleanup says so with its exit code, not
// with Main's, which the tests would take for a clean run, and prints what
// failed.
func TestAChildWhoseCleanupFailsSaysSo(t *testing.T) {
	hosttest.New(t)
	cmd, out := child(t, nil, "cleanup-fails", "version")
	if code := exitOf(t, cmd); code != childFailed || !strings.Contains(out.String(), "the scene's cleanup failed") {
		t.Errorf("a child whose cleanup failed exited with %d, want %d, and said:\n%s", code, childFailed, out.String())
	}
}

// The child of the signal tests leaves no test host behind: its folder is
// gone once the child has exited.
func TestTheChildLeavesNoTestHost(t *testing.T) {
	hosttest.New(t)
	cmd, stderr := child(t, nil, "setup-trust", "version")
	if code := exitOf(t, cmd); code != 0 {
		t.Fatalf("version in the child: exit %d:\n%s", code, stderr.String())
	}
	_, rest, _ := strings.Cut(stderr.String(), "test host: ")
	root, _, _ := strings.Cut(rest, "\n")
	if root == "" {
		t.Fatalf("the child named no test host:\n%s", stderr.String())
	}
	if _, err := os.Stat(root); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the child left its test host %s: %v", root, err)
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
func child(t *testing.T, term *hosttest.Terminal, scene string, args ...string) (*childProc, *bytes.Buffer) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestMainInAChild$")
	cmd.Env = append(os.Environ(), mainArgsEnv+"="+strings.Join(args, mainArgsSep), mainSceneEnv+"="+scene, "NO_COLOR=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stderr
	if term != nil {
		cmd.Stdin, cmd.Stdout, cmd.Stderr = term.TTY, term.TTY, term.TTY
	}
	return startChild(t, cmd), &stderr
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
		cmd := exec.Command("sh", "-c", `touch "$1"; while :; do echo noise >&2; done`, "sh", started)
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

// SIGINT and SIGTERM end a command with 130 and an error that says so, and
// the client prints it (docs/contributing/cli-spec.md 5.1).
func TestSignalsCancel(t *testing.T) {
	hosttest.New(t)
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	script := filepath.Join(dir, "provider")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+started+"\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := client.SaveHostsContext(context.Background(), []client.Host{{Name: "box", SSH: "box", Provider: &client.Provider{Script: script}}}); err != nil {
		t.Fatal(err)
	}
	for _, sig := range []os.Signal{syscall.SIGTERM, os.Interrupt} {
		_ = os.Remove(started)
		cmd, stderr := child(t, nil, "", "host", "start", "box")
		for deadline := time.Now().Add(childWait); ; time.Sleep(20 * time.Millisecond) {
			if _, err := os.Stat(started); err == nil {
				break
			}
			if time.Now().After(deadline) {
				_ = cmd.Process.Kill()
				<-cmd.done
				t.Fatalf("%v: the provider script did not start within %s, stderr:\n%s", sig, childWait, stderr.String())
			}
		}
		if err := cmd.Process.Signal(sig); err != nil {
			t.Fatal(err)
		}
		if code := exitOf(t, cmd); code != 130 || !strings.Contains(stderr.String(), "Error: ") || !strings.Contains(stderr.String(), "\nTry: ") {
			t.Errorf("%v: exit %d, stderr:\n%s", sig, code, stderr.String())
		}
	}
}

// A signal at a question is a cancel, never an answer: the client keeps no
// answer for the repository, and exits with 130.
func TestASignalAtAQuestionKeepsNoAnswer(t *testing.T) {
	hosttest.New(t)
	term := hosttest.NewTerminal(t, 100, 30)
	cmd, _ := child(t, term, "setup-trust", "new", "box", "--dir", "~/proj", "--name", "s", "--", "sleep", "60")
	term.WaitFor("Run it? [y/N] ")
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if code := exitOf(t, cmd); code != 130 {
		t.Errorf("exit %d, want 130:\n%s", code, term.Output())
	}
	out := string(term.WaitFor("kept answers:"))
	if !strings.Contains(out, "kept answers: map[]") || !strings.Contains(out, "Error: a signal ended the command at a question") {
		t.Errorf("the terminal shows:\n%s", out)
	}
}

// A signal ends doctor keys, which waits for the terminal: it puts the
// terminal back, closes the debug file, and exits with 130.
func TestASignalEndsTheKeyTester(t *testing.T) {
	hosttest.New(t)
	file := filepath.Join(t.TempDir(), "debug.log")
	term := hosttest.NewTerminal(t, 100, 30)
	cmd, _ := child(t, term, "", "--debug-file", file, "doctor", "keys")
	term.WaitFor("Press keys.")
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if code := exitOf(t, cmd); code != 130 {
		t.Errorf("exit %d, want 130:\n%s", code, term.Output())
	}
	if out := term.WaitFor("Try: "); !bytes.Contains(out, []byte("\x1b[<u\x1b[?2004l")) {
		t.Errorf("the kitty keyboard protocol stayed on: %q", out)
	}
	// A terminal in its normal mode echoes what is typed; one left raw
	// does not.
	term.Clear()
	term.Type("cooked\n")
	term.WaitFor("cooked")
	if b, err := os.ReadFile(file); err != nil || !bytes.Contains(b, []byte("the command failed")) {
		t.Errorf("the debug file: %v\n%s", err, b)
	}
}
