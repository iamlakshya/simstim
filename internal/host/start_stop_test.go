package host

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// A start whose secrets did not arrive deletes the session only once its
// holder is known to have stopped (cli-spec 9.5). With a unit, systemd must
// stop it and say that it is no longer active: a stop that fails, a
// question that fails, or a unit that stays active is an error.
func TestStopStartingNeedsTheStop(t *testing.T) {
	old := systemctl
	t.Cleanup(func() { systemctl = old })
	m := &Manifest{ID: "abcd2345", Run: Runtime{Unit: UnitName("abcd2345")}}
	for name, c := range map[string]struct {
		stop, show error
		state      string
		ok         bool
	}{
		// A unit that systemd already removed, as after its holder crashed:
		// the stop fails, and the unit is inactive, so it stopped.
		"the stop fails, and the unit is gone": {stop: errors.New("systemctl --user stop: exit status 5"), state: "inactive", ok: true},
		"the stop fails, and the unit stays":   {stop: errors.New("systemctl --user stop: exit status 1"), state: "active"},
		"the question fails":                   {show: errors.New("systemctl --user show: exit status 1")},
		"the unit stays":                       {state: "active"},
		"the unit stops":                       {state: "inactive", ok: true},
		"the unit stops badly":                 {state: "failed", ok: true},
	} {
		var asked []string
		systemctl = func(_ context.Context, args ...string) ([]byte, error) {
			asked = append(asked, args[0])
			switch args[0] {
			case "stop":
				return nil, c.stop
			case "show":
				return []byte(c.state + "\n"), c.show
			}
			return nil, errors.New("unexpected " + strings.Join(args, " "))
		}
		start := time.Now()
		err := stopStarting(m, nil)
		if (err == nil) != c.ok || asked[0] != "stop" {
			t.Errorf("%s: %v, after %v", name, err, asked)
		}
		if c.state == "active" && time.Since(start) < 3*time.Second {
			t.Errorf("%s: it gave up after %v", name, time.Since(start))
		}
	}
	if err := stopStarting(&Manifest{ID: "abcd2345"}, nil); err == nil {
		t.Error("a start with neither a unit nor a process stopped nothing, and said that it did")
	}
}

// A holder on a host with cgroup v1, whose cgroup file shows no unit,
// learns its unit from launchHolder, and records it in its first save and
// in every later save. A variable that names another session's unit counts
// for nothing.
func TestTheHolderLearnsItsUnit(t *testing.T) {
	const id = testHolderID
	fakeProc(t, map[string]string{"self/cgroup": "12:memory:/user.slice/user-UID.slice/user@UID.service/" + UnitName(id) + "\n1:name=systemd:/user.slice\n"})
	t.Setenv(unitEnv, UnitName("zzzz2345"))
	if u := holderUnit(id); u != "" {
		t.Fatalf("the unit of another session: %q", u)
	}
	t.Setenv(unitEnv, UnitName(id))
	h := newTestHolder(t)
	h.m.Run.Unit = holderUnit(id)
	for i := range 2 {
		h.mu.Lock()
		h.m.Status.Reason = "save " + string(rune('a'+i)) // a change for each save
		c := h.copyForSave()
		h.mu.Unlock()
		if err := h.save(c, false); err != nil {
			t.Fatal(err)
		}
		if m, err := LoadManifest(id); err != nil || m.Run.Unit != UnitName(id) {
			t.Fatalf("save %d: %+v, %v", i, m, err)
		}
	}
	// With cgroup v2, the cgroup file names the unit.
	fakeProc(t, map[string]string{"self/cgroup": "0::/user.slice/user-UID.slice/user@UID.service/app.slice/" + UnitName(id) + "\n"})
	t.Setenv(unitEnv, "")
	if u := holderUnit(id); u != UnitName(id) {
		t.Errorf("cgroup v2: %q", u)
	}
}
