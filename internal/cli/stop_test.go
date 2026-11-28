package cli

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
)

func TestStopCommandLifecycle(t *testing.T) {
	newTestHost(t)
	id := newSession(t, "worker", "sleep", "60")

	out := okSaid(t, "stop", "box/worker")
	if want := "Stopped worker (" + id + ") on box. Resume it with " + brand.Name + " resume box/" + id + ".\n"; out != want {
		t.Fatalf("stop printed %q, want %q", out, want)
	}
	if s := sessionState(t, id); s != api.StateStopped {
		t.Fatalf("state after stop = %q, want %q", s, api.StateStopped)
	}
	if ls := ok(t, "ls"); !strings.Contains(ls, id) || !strings.Contains(ls, "stopped") {
		t.Fatalf("ls after stop = %q", ls)
	}

	// Stopping again is idempotent.
	again := okSaid(t, "stop", "box/worker")
	if want := "worker (" + id + ") on box is already stopped.\n"; again != want {
		t.Fatalf("second stop printed %q, want %q", again, want)
	}

	// stop --json outputs a v1 session JSON object.
	raw := ok(t, "stop", "box/worker", "--json")
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		t.Fatalf("stop --json invalid JSON: %v\n%s", err, raw)
	}
	if obj["v"] != float64(1) || obj["id"] != id || obj["state"] != api.StateStopped || obj["alive"] != false {
		t.Fatalf("stop --json = %+v", obj)
	}

	// Without a terminal, attach fails without waking the stopped session.
	failsWithHint(t, "attach needs a terminal", "send ", "attach", "box/worker")
	if s := sessionState(t, id); s != api.StateStopped {
		t.Fatalf("non-terminal attach woke stopped session: state = %q", s)
	}

	// host restore --all restores the stopped session.
	restored := ok(t, "host", "restore", "box", "--all")
	if !strings.Contains(restored, id) || !strings.Contains(restored, "restored") {
		t.Fatalf("host restore --all = %q", restored)
	}
	if s := sessionState(t, id); s == api.StateStopped {
		t.Fatalf("state after host restore --all = %q", s)
	}

	// Stop again, then resume brings it back.
	okSaid(t, "stop", "box/worker")
	res := okSaid(t, "resume", "box/worker", "--detach")
	if !strings.Contains(res, "Resumed box/"+id) {
		t.Fatalf("resume after stop = %q", res)
	}
	if s := sessionState(t, id); s == api.StateStopped {
		t.Fatalf("state after resume = %q", s)
	}

	// Destroy removes the session and its folder completely.
	okSaid(t, "kill", "box/worker")
	if _, err := os.Stat(paths.SessionDir(id)); !os.IsNotExist(err) {
		t.Fatalf("session folder still exists after kill: %v", err)
	}
	if ls := ok(t, "ls"); strings.Contains(ls, id) {
		t.Fatalf("ls still lists deleted session:\n%s", ls)
	}
}

func TestAttachWakesStoppedSession(t *testing.T) {
	newTestHost(t)
	m := startedRE.FindStringSubmatch(okSaid(t, "new", "box", "--detach", "--name", "sleepy"))
	if m == nil {
		t.Fatal("new started no session")
	}
	id := m[1]
	okSaid(t, "stop", "box/"+id)
	if s := sessionState(t, id); s != api.StateStopped {
		t.Fatalf("state after stop = %q", s)
	}

	term, done := inTerminal(t, "attach", "box/"+id)
	term.WaitFor("\x1b[22;0t")
	term.Type("echo WOKE-$((3+4))\r")
	term.WaitFor("WOKE-7")
	term.Type("\x1c")
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
	term.WaitFor("Detached. sleepy keeps running on box.")
}

func TestStopWithAnOldHelperAndMissingSession(t *testing.T) {
	newTestHost(t)
	id := newSession(t, "old", "sleep", "60")
	failsWithHint(t, `no session "nosuch" on box`, "ls box", "stop", "box/nosuch")

	old := host.Features
	host.Features = slices.DeleteFunc(slices.Clone(old), func(f string) bool { return f == "stop" })
	t.Cleanup(func() { host.Features = old })
	failsWithHint(t, "the helper on box is too old to stop a session", "host add box", "stop", "box/"+id)
}
