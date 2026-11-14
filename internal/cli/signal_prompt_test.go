//go:build darwin || linux

package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	xterm "golang.org/x/term"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

// childOnTerminal runs Main with args in a child process, with a new
// terminal as its stdin, stdout and stderr, so that input is on.
func childOnTerminal(t *testing.T, args ...string) (*hosttest.Terminal, *childProc) {
	t.Helper()
	term := hosttest.NewTerminal(t, 90, 20)
	cmd := exec.Command(os.Args[0], "-test.run=^TestMainInAChild$")
	cmd.Env = append(os.Environ(), mainArgsEnv+"="+strings.Join(args, mainArgsSep), "NO_COLOR=1", "TERM=xterm-256color")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = term.TTY, term.TTY, term.TTY
	return term, startChild(t, cmd)
}

// cooked reports whether the terminal is back in its line mode, with echo.
func cooked(t *testing.T, term *hosttest.Terminal) bool {
	t.Helper()
	tio, err := unix.IoctlGetTermios(int(term.TTY.Fd()), ioctlGetTermios)
	if err != nil {
		t.Fatal(err)
	}
	return tio.Lflag&unix.ICANON != 0 && tio.Lflag&unix.ECHO != 0
}

// A SIGHUP between the restore of the terminal and the stop of the
// question's signal handler is a cancel, never an answer: the question
// ends as the picker does, with a last look for a signal after the stop.
// The test catches the signal too, so a signal that the question misses
// does not end the test.
func TestASignalAsAQuestionEndsIsACancel(t *testing.T) {
	withSignal(t)
	caught := make(chan os.Signal, 1)
	signal.Notify(caught, syscall.SIGHUP)
	defer signal.Stop(caught)
	defer func() { promptStage = func(string) {} }()
	promptStage = func(at string) {
		if at != "restored" {
			return
		}
		if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
			t.Error(err)
		}
		select {
		case <-caught:
		case <-time.After(5 * time.Second):
			t.Error("the signal never came")
		}
	}
	term := hosttest.NewTerminal(t, 80, 10)
	before, err := xterm.GetState(int(term.TTY.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	got := askOn(t, term)
	term.WaitFor("Name? ")
	term.Type("yes\r")
	if a := waitAnswer(t, term, got); !errors.Is(a.err, errInterrupted) {
		t.Errorf("a signal as the question ended: %q, %v, want a cancel", a.text, a.err)
	}
	// Only listen closes interrupted, and only after it has ended the
	// command's context (endAtSignal).
	if signalled() {
		t.Error("the question closed interrupted")
	}
	after, err := xterm.GetState(int(term.TTY.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Error("the question left the terminal changed")
	}
}

// panicWriter panics at its first write.
type panicWriter struct{}

func (panicWriter) Write([]byte) (int, error) { panic("the echo failed") }

// A panic at a question, while the terminal is raw, restores the terminal:
// the restore is deferred.
func TestAPanicAtAQuestionRestoresTheTerminal(t *testing.T) {
	withSignal(t)
	term := hosttest.NewTerminal(t, 80, 10)
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the echo did not panic")
			}
		}()
		_, _ = answerOn(context.Background(), term.TTY, panicWriter{}, "Name? ")
	}()
	if !cooked(t, term) {
		t.Error("after a panic at a question the terminal is still raw")
	}
}

// A signal from outside, at a question in raw mode, restores the terminal,
// and the run exits with 130 (cli-spec 6.2, rule 6).
func TestSignalAtAQuestionRestoresTheTerminal(t *testing.T) {
	hosttest.New(t)
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT} {
		_ = os.MkdirAll(filepath.Join(client.ConfigDir(), "ssh"), 0o700)
		_ = os.WriteFile(filepath.Join(client.ConfigDir(), "ssh", "config-gone"), []byte("# generated\n"), 0o600)
		term, cmd := childOnTerminal(t, "doctor", "--fix")
		term.WaitFor("Fix it? [y/N] ")
		time.Sleep(100 * time.Millisecond) // the question reads in raw mode by now
		if cooked(t, term) {
			t.Fatalf("%v: the question does not read in raw mode", sig)
		}
		if err := cmd.Process.Signal(sig); err != nil {
			t.Fatal(err)
		}
		if code := exitOf(t, cmd); code != 130 {
			t.Errorf("%v at a question: exit %d, the terminal shows %q", sig, code, term.Output())
		}
		if !cooked(t, term) {
			t.Errorf("%v at a question left the terminal raw", sig)
		}
		if _, err := os.Stat(filepath.Join(client.ConfigDir(), "ssh", "config-gone")); err != nil {
			t.Errorf("%v at a question: the fix ran", sig)
		}
	}
}

// A signal from outside at the picker restores the terminal, and the run
// exits with 130.
func TestSignalAtThePickerRestoresTheTerminal(t *testing.T) {
	h := hosttest.New(t)
	h.Script("ssh", "exit 255\n")
	t.Setenv("PATH", filepath.Join(h.Root, "bin")+":"+os.Getenv("PATH"))
	if err := client.SaveHosts(context.Background(), []client.Host{{Name: "box", SSH: "box"}}); err != nil {
		t.Fatal(err)
	}
	cache := `{"sessions":{"box":[{"id":"zzz","name":"cached-one","agent":"claude","state":"idle","created_at":"2026-11-10T10:00:00Z","alive":true}]}}`
	_ = os.MkdirAll(client.StateDir(), 0o700)
	if err := os.WriteFile(filepath.Join(client.StateDir(), "launcher.json"), []byte(cache), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT} {
		term, cmd := childOnTerminal(t, "attach")
		term.WaitFor("box/cached-one")
		time.Sleep(100 * time.Millisecond)
		if err := cmd.Process.Signal(sig); err != nil {
			t.Fatal(err)
		}
		if code := exitOf(t, cmd); code != 130 {
			t.Errorf("%v at the picker: exit %d, the terminal shows %q", sig, code, term.Output())
		}
		if !cooked(t, term) {
			t.Errorf("%v at the picker left the terminal raw", sig)
		}
	}
}
