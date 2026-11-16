//go:build unix

package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/muesli/cancelreader"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

// The key tester reads through its fake, asks it to cancel at the end of
// its context, ends although the cancel fails, and puts the terminal back.
func TestTheKeyTesterEndsWhenItsReadCannotBeCancelled(t *testing.T) {
	newTestHost(t)
	withSignal(t)
	f := &fakeKeys{started: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
	defer close(f.release)
	old := newKeyReader
	newKeyReader = func(*os.File) (cancelreader.CancelReader, error) { return f, nil }
	defer func() { newKeyReader = old }()
	term := hosttest.NewTerminal(t, 100, 30)
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = term.TTY, term.TTY
	defer func() { os.Stdin, os.Stdout = oldIn, oldOut }()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- keyTester(ctx) }()
	select {
	case <-f.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the key tester never read its input")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("keyTester = %v, want the context's end", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the key tester still waits for a read that cannot be cancelled")
	}
	select {
	case <-f.cancelled:
	default:
		t.Error("the key tester did not ask its read to cancel")
	}
	term.WaitFor("\x1b[<u\x1b[?2004l")
	term.Clear()
	term.Type("cooked\n")
	term.WaitFor("cooked")
}

// A key of the configuration that does not parse is the user's to fix: a
// usage error that names the setting.
func TestTheKeyTesterSaysWhichKeyIsWrong(t *testing.T) {
	newTestHome(t)
	cfg := client.ConfigDir()
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg+"/config.toml", []byte("[attach]\ndetach_key = \"oops\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, done := inTerminal(t, "doctor", "keys")
	err := wait(t, done)
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "bad_config" || ue.Hint != "Fix attach.detach_key in the config." || printError(io.Discard, err, nil, false, false) != 2 {
		t.Errorf("doctor keys with a bad detach key = %+v", err)
	}
}
