//go:build unix

package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

// A child's failed run exits with childFailed, a code that Main never has.
func init() {
	mainChildScenes["setup-trust"] = func(t *testing.T) func() {
		th := newTestHost(t)
		fmt.Fprintf(os.Stderr, "test host: %s\n", th.h.Root)
		hook := filepath.Join(th.h.Home, "proj", "."+brand.Name, "setup")
		if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(hook, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return func() { fmt.Printf("\r\nkept answers: %v\r\n", keptAnswers("box")) }
	}
	if os.Getenv(mainArgsEnv) != "" {
		hosttest.ExitCode = func(code int) int {
			if code != 0 {
				return childFailed
			}
			return code
		}
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
func child(t *testing.T, term *hosttest.Terminal, scene string, args ...string) (*childProc, *bytes.Buffer) {
	t.Helper()
	cmd, stderr := childCommand(scene, args...)
	if term != nil {
		cmd.Stdin, cmd.Stdout, cmd.Stderr = term.TTY, term.TTY, term.TTY
	}
	return startChild(t, cmd), stderr
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
