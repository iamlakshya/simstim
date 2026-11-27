//go:build unix

package cli

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/testhome"
)

// A secret's command gets the terminal when input is not turned off, and
// stdin and stderr are terminals, also when stdout goes to a file: at new
// and at resume. With input off it gets no terminal.
func TestSecretCommandsGetTheTerminal(t *testing.T) {
	th := newTestHost(t)
	t.Setenv(noInputEnv, "")
	where := filepath.Join(th.h.Home, "where")
	writeSettings(t, "[secrets]\nTOK = \"if [ -t 0 ]; then echo tty >> '"+where+"'; else echo none >> '"+where+"'; fi; echo value\"\n")
	term := hosttest.NewTerminal(t, 80, 10)
	out, err := os.Create(filepath.Join(t.TempDir(), "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	atTerminal := func(args ...string) {
		t.Helper()
		oldIn, oldOut, oldErr := os.Stdin, os.Stdout, os.Stderr
		os.Stdin, os.Stdout, os.Stderr = term.TTY, out, term.TTY
		root := Root()
		root.SetArgs(args)
		err := root.ExecuteContext(withLine(context.Background(), args))
		os.Stdin, os.Stdout, os.Stderr = oldIn, oldOut, oldErr
		if err != nil {
			t.Fatalf("%s: %v", args, err)
		}
	}
	atTerminal("new", "box", "--detach", "--wall", "none", "--downgrade", "--name", "one", "--secret", "TOK", "--", "sleep", "60")
	atTerminal("new", "box", "--detach", "--wall", "none", "--downgrade", "--name", "two", "--secret", "TOK", "--no-input", "--", "sleep", "60")
	m := startedRE.FindStringSubmatch(okSaid(t, "new", "box", "--detach", "--wall", "none", "--downgrade", "--name", "three", "--secret", "TOK", "--", "/bin/sh", "-c", "exit 0"))
	if m == nil {
		t.Fatal("new started no session")
	}
	agentEnded(t, m[1])
	atTerminal("resume", "box/"+m[1], "--detach", "--secret", "TOK")
	// Input off at the terminal: with --no-input, and with --json.
	agentEnded(t, m[1])
	atTerminal("resume", "box/"+m[1], "--detach", "--secret", "TOK", "--no-input")
	agentEnded(t, m[1])
	atTerminal("resume", "box/"+m[1], "--detach", "--secret", "TOK", "--json")
	got, _ := os.ReadFile(where)
	if string(got) != "tty\nnone\nnone\ntty\nnone\nnone\n" {
		t.Errorf("the secret's command had a terminal at new, new --no-input, new from a pipe, resume, resume --no-input "+
			"and resume --json: %q, want tty, none, none, tty, none, none", got)
	}
}

// A cancel during the local fixes says what was done before it, as every
// cancel does, and never that nothing changed after a change: on the
// terminal, a fix that ran by its problem, and a fix that the cancel
// stopped part way by how far it got; in the data of --json, each fix as
// it ended, with each file it removed. It speaks of the fixes, not of a
// question. The cancel comes between two whole fixes, and between two
// files of one fix.
func TestACancelDuringTheFixesSaysWhatWasFixed(t *testing.T) {
	t.Run("between two fixes", func(t *testing.T) {
		hosttest.New(t)
		short, err := os.MkdirTemp(testhome.Root(), "f")
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv("XDG_CACHE_HOME", short) // a socket's path in it must be short
		gone := writeFixWrapper(t, "gone")
		controlHost := client.Host{Name: "control-fixture", SSH: "control-fixture.invalid"}
		if err := client.SaveHostsContext(context.Background(), []client.Host{controlHost}); err != nil {
			t.Fatal(err)
		}
		pinFor(t, controlHost.Name)
		cmd, err := (&client.SSH{Host: controlHost}).Command(context.Background(), "true")
		if err != nil {
			t.Fatal(err)
		}
		var pattern string
		for _, arg := range cmd.Args {
			if strings.HasPrefix(arg, "ControlPath=") {
				pattern = strings.TrimPrefix(arg, "ControlPath=")
			}
		}
		if pattern == "" || pattern == "none" || !strings.Contains(pattern, "%C") {
			t.Fatalf("canonical controller did not publish an indexed control path: %q", cmd.Args)
		}
		dead := strings.ReplaceAll(pattern, "%C", strings.Repeat("a", 40))
		l, err := net.Listen("unix", dead)
		if err != nil {
			t.Fatal(err)
		}
		l.(*net.UnixListener).SetUnlinkOnClose(false)
		_ = l.Close()
		res, err := applyLocal(cancelAtTheFirstFile(t), []string{"ssh-configs", "control-sockets"})
		if line := cancelLineOf(t, err); line != "Cancelled during the fixes. Done before that: fixed: 1 generated SSH config(s) for hosts you removed.\n" {
			t.Errorf("a cancel between two fixes: %q", line)
		}
		if fixes := cancelledFixes(t, err); len(fixes) != 2 || !fixes[0].Done || fixes[1].Done || len(fixes[1].Changed) != 0 {
			t.Errorf("the fixes in the JSON error: %+v", fixes)
		}
		if len(res) != 2 || !res[0].Done || res[1].Done {
			t.Errorf("the fixes after a cancel between them: %+v", res)
		}
		if _, err := os.Stat(gone); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the completed first fix left its wrapper: %v", err)
		}
		if _, err := os.Stat(dead); err != nil {
			t.Errorf("the second fix ran after the cancel: %v", err)
		}
	})
	t.Run("between two files", func(t *testing.T) {
		hosttest.New(t)
		var gone []string
		for _, name := range []string{"gone1", "gone2"} {
			gone = append(gone, writeFixWrapper(t, name))
		}
		_, err := applyLocal(cancelAtTheFirstFile(t), []string{"ssh-configs"})
		if line := cancelLineOf(t, err); line != "Cancelled during the fixes. Done before that: removed 1 of 2 generated SSH configs.\n" {
			t.Errorf("a cancel after the first file: %q", line)
		}
		if fixes := cancelledFixes(t, err); len(fixes) != 1 || fixes[0].Done || !slices.Equal(fixes[0].Changed, []string{"removed " + gone[0]}) {
			t.Errorf("the fixes in the JSON error: %+v", fixes)
		}
		if _, err := os.Stat(gone[1]); err != nil {
			t.Errorf("the second file went after the cancel: %v", err)
		}
	})
}

// host add with --no-input takes the checklist's default: it offers no
// agent, and says how to install each.
// The command runs at a terminal, stdin, stdout and stderr, so input would
// be on without the flag.
func TestHostAddChecklistNoInputCommand(t *testing.T) {
	th := newTestHost(t)
	th.h.Install(client.HelperVersion(), "")
	t.Setenv(noInputEnv, "")
	term, done := inTerminal(t, "host", "add", "added", "box", "--tier", "own", "--no-sandbox", "--no-input")
	term.WaitFor("Agents:")
	// An Enter would answer the checklist, if the command asked it.
	term.Type("\r")
	if err := wait(t, done); err != nil {
		t.Fatalf("host add --no-input: %v\n%s", err, term.Output())
	}
	out := string(term.WaitFor("added is ready."))
	if strings.Contains(out, "Type the numbers") || !strings.Contains(out, "agents install claude added") {
		t.Errorf("host add --no-input at a terminal offered the checklist:\n%s", out)
	}
}
