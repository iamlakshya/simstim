package cli

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/proto"
)

func runInBackground(root *cobra.Command, args []string) (chan error, func()) {
	ctx, cancel := context.WithCancel(withLine(context.Background(), args))
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		done <- root.ExecuteContext(ctx)
	}()
	return done, func() {
		cancel()
		<-finished
	}
}
func wait(t *testing.T, done chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(20 * time.Second):
		t.Fatal("the command did not end")
		return nil
	}
}

// An end with a known next step says it, as an error does (AGENTS.md rule 7).
func TestEndMessage(t *testing.T) {
	for _, c := range []struct {
		e    proto.Ended
		want string
	}{
		{proto.Ended{ExitCode: 9}, "\r\nThe session app on box ended (exit code 9).\n"},
		{proto.Ended{ExitCode: 137, Signal: "killed"}, "\r\nThe session app on box ended (killed).\n"},
		{proto.Ended{ExitCode: -1}, "\r\nThe session app on box stopped.\n"},
		{proto.Ended{ExitCode: 137, Signal: "killed", OOM: true},
			"\r\nThe session app on box ended (out of memory).\n\nTry: " + client.EndHint(&proto.Ended{OOM: true}, "box/a1") + "\n"},
	} {
		if got := endMessage("app", "box", "a1", &c.e); got != c.want {
			t.Errorf("%+v: %q, want %q", c.e, got, c.want)
		}
	}
	if client.EndHint(&proto.Ended{OOM: true}, "") == "" || client.EndHint(&proto.Ended{ExitCode: 1}, "box/a1") != "" || client.EndHint(nil, "") != "" {
		t.Error("only an end out of memory has a next step")
	}
}
func TestTerminalCommandsNeedATerminal(t *testing.T) {
	newTestHome(t)
	r, w, _ := os.Pipe()
	defer r.Close()
	defer w.Close()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()
	fails(t, "needs a terminal", "doctor", "keys")
	fails(t, "needs a terminal", "doctor", "terminal")
}
