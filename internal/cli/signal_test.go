package cli

import (
	"bytes"
	"fmt"
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
)

// TestMainInAChild runs Main in a child process for the signal tests. In
// a normal run it does nothing. The scene "setup-trust" starts a test host
// with a repository whose setup script needs an answer, and prints the
// answers that the client keeps after Main returns.
func TestMainInAChild(t *testing.T) {
	args := os.Getenv(mainArgsEnv)
	if args == "" {
		return
	}
	scene := os.Getenv(mainSceneEnv)
	if scene == "setup-trust" {
		th := newTestHost(t)
		hook := filepath.Join(th.h.Home, "proj", "."+brand.Name, "setup")
		if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(hook, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.Args = append([]string{"client"}, strings.Split(args, mainArgsSep)...)
	code := Main()
	if scene == "setup-trust" {
		fmt.Printf("\r\nkept answers: %v\r\n", keptAnswers("box"))
	}
	os.Exit(code)
}

// child starts the test binary as a client that runs args. With term, the
// terminal is its stdin, stdout and stderr.
func child(t *testing.T, term *hosttest.Terminal, scene string, args ...string) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestMainInAChild$")
	cmd.Env = append(os.Environ(), mainArgsEnv+"="+strings.Join(args, mainArgsSep), mainSceneEnv+"="+scene, "NO_COLOR=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if term != nil {
		cmd.Stdin, cmd.Stdout, cmd.Stderr = term.TTY, term.TTY, term.TTY
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd, &stderr
}

// childWait is how long a test waits for a child. A child takes about a
// second, but a busy machine can make it far slower.
const childWait = 60 * time.Second

func exitOf(t *testing.T, cmd *exec.Cmd) int {
	t.Helper()
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(childWait):
		_ = cmd.Process.Kill()
		t.Fatal("the client did not end after the signal")
	}
	return cmd.ProcessState.ExitCode()
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
	if err := client.SaveHosts([]client.Host{{Name: "box", SSH: "box", Provider: &client.Provider{Script: script}}}); err != nil {
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
				_ = cmd.Wait()
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
