//go:build unix

package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

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
