package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

// More checks of the session verbs: what a host sends is cleaned before
// it is drawn, a race with another client, a failed start, and the old
// name without a session.

// verbHostile holds the controls that a host could put in a session's words:
// a full reset (RIS), a clipboard write (OSC 52), a bell and a bidi
// override.
const verbHostile = "\x1bc\x1b]52;c;aGk=\x07\u202e"

// clean reports whether s holds none of verbHostile's controls.
func clean(s string) bool { return !strings.ContainsAny(s, "\x1b\x07\u202e") }

// The questions of rm and attach, and the text of show, clean every word
// that a host sends (major X3).
func TestHostWordsAreCleaned(t *testing.T) {
	s := client.Session{ID: "1a2b" + verbHostile, Name: "auth" + verbHostile, Agent: "claude" + verbHostile, Cwd: "/srv/app" + verbHostile,
		State: "errored", Source: "process" + verbHostile, Reason: "it failed" + verbHostile, Signal: "killed" + verbHostile, ExitCode: new(1),
		HostVersion: "0.2.0" + verbHostile, Unit: "unit" + verbHostile, Secrets: []string{"TOKEN" + verbHostile}, MissingSecrets: []string{"KEY" + verbHostile}}
	running := s
	running.State = "working"
	for name, ask := range map[string]func(p *prompter) (bool, error){
		"rm":                     func(p *prompter) (bool, error) { return askDelete(p, "box", running) },
		"rm, an ended agent":     func(p *prompter) (bool, error) { return askDelete(p, "box", s) },
		"attach, an ended agent": func(p *prompter) (bool, error) { return askRestart(p, "box", s) },
	} {
		var out bytes.Buffer
		if _, err := ask(testPrompts("n\n", &out)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !clean(out.String()) || !strings.Contains(out.String(), "auth") && !strings.Contains(out.String(), "claude") {
			t.Errorf("%s asked %q", name, out.String())
		}
	}
	if got := describeSession("box", s); !clean(got) || !strings.Contains(got, "it failed") {
		t.Errorf("show printed %q", got)
	}
	// The question's text when input is off, as the error shows it.
	p := quietPrompts(context.Background())
	_, err := askDelete(p, "box", running)
	if got, _ := reportOf(err); !clean(got) {
		t.Errorf("the error printed %q", got)
	}
}

// The attach itself is byte-exact: a session's own bytes, controls and
// all, reach the terminal as they are. Only the client's own lines are
// cleaned.
func TestAttachKeepsTheSessionsBytes(t *testing.T) {
	newTestHost(t)
	t.Setenv(noInputEnv, "")
	newSession(t, "raw", "/bin/sh", "-c", `printf '\033]52;c;aGk=\007\342\200\256MARK-RAW'; exec sleep 60`)
	term, done := inTerminal(t, "attach", "box/raw")
	term.WaitFor("\x1b]52;c;aGk=\x07\u202eMARK-RAW")
	term.Type("\x1c")
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
}

// Another client starts the agent again between this client's list and
// its resume (X4): the host refuses the second resume because the agent
// runs, and the command goes on as on a running agent.
func TestAnotherClientResumesFirst(t *testing.T) {
	th := newTestHost(t)
	t.Setenv(noInputEnv, "")
	other := func(f found) {
		otherClient(t, th, func(ctx context.Context, conn *client.Conn) error {
			_, err := conn.Resume(ctx, client.ResumeArgs{ID: f.s.ID})
			return err
		})
	}
	old := beforeResume
	t.Cleanup(func() { beforeResume = old })

	endedOnce(t, th, "crash")
	beforeResume = other
	if out := okSaid(t, "resume", "box/crash", "--detach"); out != "crash on box is already running.\n" {
		t.Errorf("resume --detach after another client's resume: %q", out)
	}

	beforeResume = old
	endedOnce(t, th, "second")
	beforeResume = other
	term, done := inTerminal(t, "attach", "box/second")
	term.WaitFor("Start shell again in second on box? [Y/n] ")
	term.Type("\r")
	term.WaitFor("Another client started the agent of second on box again first, so this attaches.")
	term.WaitFor("SECOND-21")
	term.Type("\x1c")
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
}

// show keeps an agent's reason when the state line does not say it, such
// as the error of a start that failed (X6).
func TestShowAFailedStart(t *testing.T) {
	newTestHost(t)
	// new fails, and the host keeps the session, with why it ended.
	if _, err := run(t, "new", "box", "--detach", "--wall", "none", "--downgrade", "--name", "bad", "--", "/no/such/agent-xyz"); err == nil {
		t.Fatal("new started a command that does not exist")
	}
	hosttest.WaitFor(t, 10*time.Second, "the failed session in ls", func() bool { return strings.Contains(ok(t, "ls"), " bad ") })
	out := ok(t, "show", "box/bad")
	if !strings.Contains(out, "  Reason          ") || !strings.Contains(out, "/no/such/agent-xyz") {
		t.Errorf("show of a failed start:\n%s", out)
	}
}

// kill without a session warns too, and teaches rm: its missing session is
// rm's, with rm's usage and example.
func TestKillWithoutASession(t *testing.T) {
	newTestHost(t)
	out, errOut, code := mainRun(t, "kill")
	if code != 2 || out != "" || strings.Count(errOut, `Warning: "kill" is now "rm".`) != 1 ||
		!strings.Contains(errOut, "Error: rm needs a session, as <host>/<session>.") || !strings.Contains(errOut, "Example: "+brand.Name+" rm my-vps/auth-fix") {
		t.Errorf("kill: exit %d\n%s", code, errOut)
	}
}
