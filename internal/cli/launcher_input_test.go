//go:build !windows

package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

func runWithStreams(args []string, in, out, errOut *os.File) error {
	oldIn, oldOut, oldErr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = in, out, errOut
	defer func() { os.Stdin, os.Stdout, os.Stderr = oldIn, oldOut, oldErr }()
	root := Root()
	root.SetArgs(args)
	return root.ExecuteContext(withLine(context.Background(), args))
}

// The interactive launcher entry points (<root>, flip, demo) refuse to open
// when --no-input or the no-input variable turns input off, or when stderr is
// redirected away from the terminal.
func TestLauncherEntryPointsRespectNoInputAndStderrTerminal(t *testing.T) {
	hosttest.New(t)
	term := hosttest.NewTerminal(t, 80, 24)
	redirected, err := os.Create(filepath.Join(t.TempDir(), "stderr.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer redirected.Close()

	commands := []struct {
		args []string
		what string
		hint string
		term string
	}{
		{nil, "the launcher cannot open with input off", "ls", "the launcher needs a terminal"},
		{[]string{"flip"}, "the switcher cannot open with input off", "flip <host>/<session>", "the switcher needs a terminal"},
		{[]string{"demo"}, "the demo cannot open with input off", "demo", "the demo needs a terminal"},
	}

	t.Setenv(noInputEnv, "")
	for _, c := range commands {
		args := append(append([]string(nil), c.args...), "--no-input")
		err := runWithStreams(args, term.TTY, term.TTY, term.TTY)
		var ue *client.UserError
		if !errors.As(err, &ue) || ue.Code != "no_input" || ue.Kind != client.KindNeedsAnswer || ue.Msg != c.what || !strings.Contains(ue.Hint, c.hint) {
			t.Errorf("%v at terminal: err=%v, ue=%+v, want %q / %q", args, err, ue, c.what, c.hint)
		}
	}

	t.Setenv(noInputEnv, "1")
	for _, c := range commands {
		err := runWithStreams(c.args, term.TTY, term.TTY, term.TTY)
		var ue *client.UserError
		if !errors.As(err, &ue) || ue.Code != "no_input" || ue.Kind != client.KindNeedsAnswer || ue.Msg != c.what || !strings.Contains(ue.Hint, c.hint) {
			t.Errorf("%s=1 %v at terminal: err=%v, ue=%+v, want %q / %q", noInputEnv, c.args, err, ue, c.what, c.hint)
		}
	}

	t.Setenv(noInputEnv, "")
	for _, c := range commands {
		err := runWithStreams(c.args, term.TTY, term.TTY, redirected)
		var ue *client.UserError
		if !errors.As(err, &ue) || ue.Code != "no_terminal" || !strings.Contains(ue.Hint, c.hint) || ue.Msg != c.term {
			t.Errorf("%v with redirected stderr: err=%v, ue=%+v, want no_terminal and %q", c.args, err, ue, c.term)
		}
	}
}
