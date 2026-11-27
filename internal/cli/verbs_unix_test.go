//go:build unix

package cli

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// The first acceptance line of C5: kill works, warns once on stderr, and
// names rm. It never asks.
func TestKillWarnsAndNamesRm(t *testing.T) {
	newTestHost(t)
	id := newSession(t, "a", "sleep", "60")
	errOut := okSaid(t, "kill", "box/a")
	lines := strings.Split(strings.TrimSpace(errOut), "\n")
	if strings.Count(errOut, "Warning: ") != 1 || len(lines) != 2 ||
		!strings.HasPrefix(lines[0], `Warning: "kill" is now "rm". "kill" works until v0.4.0 and `) ||
		lines[1] != "Deleted a ("+id+") on box." {
		t.Errorf("kill printed %q", errOut)
	}
	// A bare name works too, and the warning names the same new form.
	newSession(t, "b", "sleep", "60")
	if errOut := okSaid(t, "kill", "b"); !strings.Contains(errOut, `"kill" is now "rm".`) || !strings.Contains(errOut, "Deleted b (") {
		t.Errorf("kill b printed %q", errOut)
	}
}

// rm with input off and without -y deletes and warns until the end of its
// old form. After the end it fails with exit code 4, and names -y.
func TestRmWithInputOff(t *testing.T) {
	newTestHost(t)
	newSession(t, "a", "sleep", "60")
	errOut := okSaid(t, "rm", "box/a")
	if !strings.HasPrefix(errOut, `Warning: without a terminal, "rm" needs -y from v0.4.0 and `) || !strings.Contains(errOut, "\nDeleted a (") {
		t.Errorf("rm without a terminal printed %q", errOut)
	}

	old := oldRmWithoutYes
	oldRmWithoutYes = nil
	t.Cleanup(func() { oldRmWithoutYes = old })
	b := newSession(t, "b", "sleep", "60")
	got, code := lineErr(t, "rm", "box/b", "--no-input")
	if code != 4 || !strings.Contains(got, "Error: rm needs a yes to delete b on box, and input is off.") ||
		!strings.Contains(got, "Try: "+brand.Name+" rm box/"+b+" --no-input -y\n") {
		t.Errorf("rm after the end: exit %d\n%s", code, got)
	}
	if !strings.Contains(ok(t, "ls"), " b ") {
		t.Error("rm after the end deleted the session")
	}
}

// rm at a terminal asks, with what stops and what stays. No keeps the
// session, and exits with 130. Yes deletes it. Without a session, rm opens
// the picker, then asks.
func TestRmAsks(t *testing.T) {
	newTestHost(t)
	t.Setenv(noInputEnv, "")
	id := newSession(t, "auth-fix", "sleep", "60")
	term, done := inTerminal(t, "rm", "auth-fix")
	term.WaitFor("Delete auth-fix on box?")
	term.WaitFor("Delete it? [y/N] ")
	term.Type("n\r")
	if err := wait(t, done); !isCancel(err) {
		t.Fatalf("no: %v", err)
	}
	if !strings.Contains(ok(t, "ls"), id) {
		t.Fatal("no deleted the session")
	}
	// The raw output, not the screen: a long folder wraps on the screen.
	if s := string(term.Output()); !strings.Contains(s, "This stops shell on box and removes the session.") || !strings.Contains(s, "stays. Unsaved work in the agent is lost.") {
		t.Errorf("the question:\n%q", s)
	}

	term, done = inTerminal(t, "rm")
	term.WaitFor("box/auth-fix")
	term.Type("\r")
	term.WaitFor("Delete it? [y/N] ")
	term.Type("y\r")
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
	term.WaitFor("Deleted auth-fix (" + id + ") on box.")
}

// endedOnce starts a session whose command exits with 3 the first time,
// and runs on the second time, as after a resume. It waits for the end.
func endedOnce(t *testing.T, th *testHost, name string) {
	t.Helper()
	marker := th.h.Home + "/" + name + "-ran"
	id := newSession(t, name, "/bin/sh", "-c", "if [ -e "+marker+" ]; then echo SECOND-$((20+1)); exec sleep 60; fi; touch "+marker+"; exit 3")
	agentEnded(t, id)
}

// The second acceptance line of C5: attach on an ended agent asks to start
// it again at a terminal. Enter takes the default, yes: attach resumes the
// agent, then attaches. No says why the agent ended, and exits with 130.
// With --no-input it fails with exit code 4 and names --restart
// (TestAttachNeedsATerminal).
func TestAttachRestartsAnEndedAgent(t *testing.T) {
	th := newTestHost(t)
	t.Setenv(noInputEnv, "")
	endedOnce(t, th, "crash")
	term, done := inTerminal(t, "attach", "box/crash")
	term.WaitFor("Start shell again in crash on box? [Y/n] ")
	term.Type("n\r")
	err := wait(t, done)
	got, code := reportOf(err)
	if code != 130 || !strings.Contains(got, "Error: the session crash on box ended (exit code 3).") || !strings.Contains(got, "Try: "+brand.Name+" resume box/") {
		t.Errorf("no: exit %d\n%s", code, got)
	}

	term, done = inTerminal(t, "attach", "crash")
	term.WaitFor("Start shell again in crash on box? [Y/n] ")
	term.Type("\r")
	term.WaitFor("SECOND-21")
	term.Type("\x1c")
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
	term.WaitFor("Detached. crash keeps running on box.")
}

// --restart answers the question: with input off, attach starts the agent
// again without asking. On an agent that runs, it says that it did not
// restart it, and attaches.
func TestAttachRestartFlag(t *testing.T) {
	th := newTestHost(t)
	t.Setenv(noInputEnv, "")
	endedOnce(t, th, "crash")
	term, done := inTerminal(t, "attach", "box/crash", "--restart", "--no-input")
	term.WaitFor("SECOND-21")
	term.Type("\x1c")
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(term.Output()), "Start shell again") {
		t.Error("attach --restart asked")
	}

	term, done = inTerminal(t, "attach", "box/crash", "--restart")
	term.WaitFor("The agent of crash on box runs, so attach did not restart it.")
	term.WaitFor("SECOND-21") // the screen, once attached
	term.Type("\x1c")
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
}

// resume on an agent that runs attaches, with a note. A flag that only a
// start reads changes nothing, and resume says so.
func TestResumeOnARunningAgent(t *testing.T) {
	newTestHost(t)
	t.Setenv(noInputEnv, "")
	newSession(t, "live", "/bin/sh", "-c", "echo LIVE-$((6*7)); exec sleep 60")
	term, done := inTerminal(t, "resume", "live", "--memory-max", "512M")
	term.WaitFor("The agent of live on box runs, so --memory-max changes nothing. Pass it again after the agent ends.")
	term.WaitFor("live on box is running, so this attaches.")
	term.WaitFor("LIVE-42")
	term.Type("\x1c")
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}

	var obj map[string]any
	if err := json.Unmarshal([]byte(okJSON(t, "resume", "box/live", "--detach", "--json")), &obj); err != nil || obj["v"] != float64(1) || obj["address"] != "box/"+obj["id"].(string) {
		t.Errorf("resume --detach --json on a running agent: %v, %v", obj, err)
	}
}

// show prints one session, as text or as JSON, and opens the picker at a
// terminal. With input off and no session, it fails with exit code 2.
func TestShowCommand(t *testing.T) {
	newTestHost(t)
	t.Setenv(noInputEnv, "")
	id := newSession(t, "auth-fix", "sleep", "60")
	out := ok(t, "show", "box/auth-fix")
	for _, want := range []string{"auth-fix on box\n", "  ID              " + id + "\n", "  Agent           shell\n", "  Attach          " + brand.Name + " attach box/" + id + "\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("show lacks %q:\n%s", want, out)
		}
	}
	if !regexp.MustCompile(`(?m)^  State           (working|idle|starting)`).MatchString(out) {
		t.Errorf("show has no state:\n%s", out)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(okJSON(t, "show", "auth-fix", "--json")), &obj); err != nil || obj["v"] != float64(1) || obj["id"] != id ||
		obj["address"] != "box/"+id || obj["host"] != "box" {
		t.Errorf("show --json: %v, %v", obj, err)
	}
	checkJSON(t, "show", okJSON(t, "show", "auth-fix", "--json"), false)

	got, code := lineErr(t, "show", "--no-input")
	if code != 2 || !strings.Contains(got, "show needs a session, as <host>/<session>") {
		t.Errorf("show --no-input: exit %d\n%s", code, got)
	}
	term, done := inTerminal(t, "show")
	term.WaitFor("box/auth-fix")
	term.Type("\r")
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
	term.WaitFor("Attach          " + brand.Name + " attach box/" + id)
}

// ls takes the host as its argument, and --host stays. Two different hosts
// are a usage error.
func TestLsTakesAHost(t *testing.T) {
	newTestHost(t)
	newSession(t, "a", "sleep", "60")
	if out := ok(t, "ls", "box"); !strings.Contains(out, " a ") {
		t.Errorf("ls box = %q", out)
	}
	if out := ok(t, "ls", "box", "--host", "box"); !strings.Contains(out, " a ") {
		t.Errorf("ls box --host box = %q", out)
	}
	got, code := lineErr(t, "ls", "box", "--host", "nuc")
	if code != 2 || !strings.Contains(got, "Error: ls got two hosts: box and nuc.") || !strings.Contains(got, "Try: "+brand.Name+" ls box\n") {
		t.Errorf("ls box --host nuc: exit %d\n%s", code, got)
	}
	got, code = lineErr(t, "ls", "bo")
	if code != 3 || !strings.Contains(got, `Did you mean "box"?`) || !strings.Contains(got, "Try: "+brand.Name+" ls box\n") {
		t.Errorf("ls bo: exit %d\n%s", code, got)
	}
}

// flip with a session runs attach, and warns.
func TestFlipWarns(t *testing.T) {
	newTestHost(t)
	newSession(t, "a", "sleep", "60")
	_, errOut, err := runApart(t, "flip", "box/a")
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Msg != "attach needs a terminal" || strings.Count(errOut, `Warning: "flip" is now "attach".`) != 1 {
		t.Errorf("flip box/a: %v, stderr %q", err, errOut)
	}
}
