//go:build darwin || linux

package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/onboard"
)

type interactiveChildFixture struct {
	m         *Model
	g         *guide
	command   *pendingExec
	cancel    context.CancelFunc
	terminal  *hosttest.Terminal
	started   string
	completed string
	gate      *os.File
	ended     chan struct{}
}

func newInteractiveChildFixture(t *testing.T, action string, wait bool, status int) *interactiveChildFixture {
	t.Helper()
	home := hosttest.New(t)
	terminal := hosttest.NewTerminal(t, 90, 30)
	dir := t.TempDir()
	f := &interactiveChildFixture{
		terminal: terminal, started: filepath.Join(dir, "started"), completed: filepath.Join(dir, "completed"),
	}
	gate := filepath.Join(home.Home, "child-release")
	if err := syscall.Mkfifo(gate, 0o600); err != nil {
		t.Fatal(err)
	}
	var err error
	f.gate, err = os.OpenFile(gate, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.gate.Close() })
	t.Setenv("TEST_CHILD_STARTED", f.started)
	t.Setenv("TEST_CHILD_COMPLETED", f.completed)
	t.Setenv("TEST_CHILD_GATE", gate)
	t.Setenv("TEST_CHILD_WAIT", map[bool]string{false: "0", true: "1"}[wait])
	t.Setenv("TEST_CHILD_EXIT", fmt.Sprint(status))
	script := `printf 'started\n' > "$TEST_CHILD_STARTED"
if [ "$TEST_CHILD_WAIT" = 1 ]; then
    IFS= read -r release < "$TEST_CHILD_GATE"
fi
printf 'completed\n' > "$TEST_CHILD_COMPLETED"
exit "$TEST_CHILD_EXIT"
`
	home.Script("ssh-keygen", script)
	editor := home.Script("context-editor", script)
	t.Setenv("PATH", filepath.Join(home.Root, "bin")+":"+os.Getenv("PATH"))
	t.Setenv("EDITOR", editor)
	t.Setenv("VISUAL", "")
	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	f.m = New(ctx, newFakeBackend(), Options{})
	f.m.handoff = true
	t.Cleanup(func() {
		cancel()
		_, _ = f.gate.WriteString("release\n")
		if f.ended != nil {
			select {
			case <-f.ended:
			case <-time.After(5 * time.Second):
				t.Error("owned interactive child did not end during cleanup")
			}
		}
		f.m.Close()
	})
	switch action {
	case "keygen":
		f.g = &guide{step: gdRentKey}
		f.m.modal = f.g
		_ = f.g.keyKey(f.m, "k")
	case "editor":
		_ = f.m.openSettings()
	default:
		t.Fatalf("unknown interactive child %q", action)
	}
	f.command = f.m.pendingExec
	if f.command == nil {
		t.Fatal("interactive command did not use the pending outer handoff")
	}
	f.command.command.in, f.command.command.out = terminal.TTY, terminal.TTY
	return f
}

func (f *interactiveChildFixture) run() <-chan error {
	done := make(chan error, 1)
	f.ended = make(chan struct{})
	go func() { defer close(f.ended); done <- f.command.command.Run() }()
	return done
}

func TestInteractiveChildDoesNotStartAfterModelCancellation(t *testing.T) {
	for _, action := range []string{"keygen", "editor"} {
		t.Run(action, func(t *testing.T) {
			f := newInteractiveChildFixture(t, action, false, 0)
			_, beforeConfig := os.Stat(client.ConfigDir())
			f.cancel()
			if err := f.command.command.Run(); !errors.Is(err, context.Canceled) {
				t.Errorf("cancelled child returned %v, want context cancellation", err)
			}
			if _, err := os.Stat(f.started); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("child started after cancellation: %v", err)
			}
			if action == "editor" && errors.Is(beforeConfig, os.ErrNotExist) {
				if _, err := os.Stat(client.ConfigDir()); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("cancelled editor created configuration folders: %v", err)
				}
			}
			const marker = "[cancelled-child-finished]"
			if _, err := f.terminal.TTY.WriteString(marker); err != nil {
				t.Fatal(err)
			}
			before, _, _ := bytes.Cut(f.terminal.WaitFor(marker), []byte(marker))
			if len(before) != 0 {
				t.Errorf("cancelled command wrote to the terminal before starting: %q", before)
			}
		})
	}
}

func TestInteractiveChildStopsWhenModelContextEnds(t *testing.T) {
	for _, action := range []string{"keygen", "editor"} {
		t.Run(action, func(t *testing.T) {
			f := newInteractiveChildFixture(t, action, true, 0)
			done := f.run()
			hosttest.WaitFor(t, 5*time.Second, "the owned child to start", func() bool {
				data, err := os.ReadFile(f.started)
				return err == nil && string(data) == "started\n"
			})
			f.cancel()
			select {
			case err := <-done:
				if err == nil {
					t.Error("cancellation returned success for a killed child")
				}
			case <-time.After(2 * time.Second):
				t.Error("the interactive child ignored model cancellation")
			}
			if _, err := os.Stat(f.completed); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the cancelled child continued to completion: %v", err)
			}
		})
	}
}

func TestInteractiveChildFailureReachesItsOwnedResult(t *testing.T) {
	for _, action := range []string{"keygen", "editor"} {
		t.Run(action, func(t *testing.T) {
			f := newInteractiveChildFixture(t, action, false, 17)
			err := f.command.command.Run()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 17 {
				t.Fatalf("child error=%v, want the command's status17", err)
			}
			result := f.command.after(err)
			if action == "editor" {
				msg, ok := result.(editorDoneMsg)
				var ue *client.UserError
				if !ok || !errors.As(msg.err, &ue) || ue.Code != "editor_failed" || ue.Hint == "" || !strings.Contains(ue.Msg, err.Error()) {
					t.Errorf("editor failure was lost: result=%+v err=%v", result, msg.err)
				}
				return
			}
			msg, ok := result.(rentKeyMsg)
			if !ok || !errors.Is(msg.err, err) {
				t.Fatalf("key-generation failure was discarded: %+v", result)
			}
			_ = f.g.onMsg(f.m, msg)
			var ue *client.UserError
			if !errors.As(f.g.err, &ue) || ue.Code != "ssh_key_failed" || ue.Hint != onboard.KeyCommand || !errors.Is(f.g.err, err) {
				t.Errorf("key-generation error has no recoverable user feedback: err=%v ue=%+v", f.g.err, ue)
			}
		})
	}
}
