//go:build unix

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

// new --detach --json needs no list of the host's sessions: its address
// is host/ID. A list that would fail changes nothing, and nothing warns.
func TestNewDetachJSONNeedsNoList(t *testing.T) {
	newTestHost(t)
	old := listSessions
	listSessions = func(context.Context, *client.Conn) ([]client.Session, error) {
		return nil, errors.New("the list broke")
	}
	defer func() { listSessions = old }()
	out, errOut, err := runApart(t, "new", "box", "--detach", "--wall", "none", "--downgrade", "--json", "--name", "api", "--", "sleep", "60")
	var s map[string]any
	if err != nil || json.Unmarshal([]byte(out), &s) != nil {
		t.Fatalf("%v\nstdout %q\nstderr %q", err, out, errOut)
	}
	var warn struct {
		V       int
		Warning map[string]string
	}
	if s["address"] != "box/"+s["id"].(string) || (errOut != "" && (json.Unmarshal([]byte(errOut), &warn) != nil || warn.Warning["code"] != "wall_downgraded")) {
		t.Errorf("stdout %s\nstderr %q", out, errOut)
	}
}

// Data goes to stdout, and every message to stderr, for each command that
// prints a line of its own when it has no data (cli-spec 7.5).
func TestStreamsOfEachCommand(t *testing.T) {
	newTestHost(t)
	newSession(t, "api", "sleep", "60")
	for _, c := range []struct {
		args []string
		said string
	}{
		{[]string{"ports", "box/api"}, "api on box listens on no TCP ports.\n"},
		{[]string{"host", "restore", "box"}, "Nothing to restore on box.\n"},
		{[]string{"host", "alerts", "log", "box"}, "box has sent no alerts yet.\n"},
	} {
		out, errOut, err := runApart(t, c.args...)
		if err != nil || out != "" || errOut != c.said {
			t.Errorf("%s: %v\nstdout %q\nstderr %q", c.args, err, out, errOut)
		}
	}
}

// A secret's command gets the terminal when input is not turned off, and
// stdin and stderr are terminals, also when stdout goes to a file.
func TestSecretTerminal(t *testing.T) {
	t.Setenv(noInputEnv, "")
	term := hosttest.NewTerminal(t, 80, 10)
	_, pw, _ := os.Pipe()
	oldIn, oldOut, oldErr := os.Stdin, os.Stdout, os.Stderr
	defer func() { os.Stdin, os.Stdout, os.Stderr = oldIn, oldOut, oldErr }()
	os.Stdin, os.Stdout, os.Stderr = term.TTY, pw, term.TTY
	root := Root()
	cmd, _, _ := root.Find([]string{"new"})
	if !secretTerminal(cmd) {
		t.Error("no terminal for a secret, with stdout in a file")
	}
	_ = cmd.ParseFlags([]string{"--no-input"})
	if secretTerminal(cmd) {
		t.Error("a terminal for a secret, with --no-input")
	}
	cmd, _, _ = Root().Find([]string{"new"})
	os.Stdin = pw
	if secretTerminal(cmd) {
		t.Error("a terminal for a secret, with stdin from a pipe")
	}
}
