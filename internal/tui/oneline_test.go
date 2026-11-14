package tui

import (
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// The add-host wizard draws the steps of the host's setup and what the
// helper said of the idle stop cleaned: no reset (ESC c), no write to the
// clipboard (OSC 52), no bidirectional control and no line break.
func TestTheWizardCleansWhatTheHostSaid(t *testing.T) {
	be := newFakeBackend()
	l := be.Local().(*fakeLocal)
	l.idleSaid = "powers off\x1bc after\x1b]52;c;aGk=\x07 30m\u202e\nidle"
	_, w := runGuideCreate(t, be)
	if w.err != nil || !w.saved {
		t.Fatalf("the create: %v, saved %v", w.err, w.saved)
	}
	found := false
	for _, c := range w.checks {
		if strings.ContainsAny(c.name+c.detail, "\x1b\x07\u202e\n") {
			t.Errorf("the check %q has %q", c.name, c.detail)
		}
		found = found || c.name == "idle stop" && c.detail == "powers offc after]52;c;aGk= 30m idle"
	}
	if !found {
		t.Errorf("no cleaned idle stop in %+v", w.checks)
	}
}

// An error of two lines, as SSH writes them, stays on one line where the
// launcher draws it on one line: the note row of a failed host in all
// sessions, and the status of the host details. Nothing starts outside
// its place, and the keys at the foot of the screen stay.
func TestATwoLineErrorOnOneLine(t *testing.T) {
	be := newFakeBackend()
	be.hosts = []client.Host{{Name: "box", SSH: "box"}, {Name: "up", SSH: "up"}}
	be.state["up"] = &fakeHost{sessions: []client.Session{{ID: "s1", Name: "work", Agent: "shell", State: api.StateIdle, Alive: true, CreatedAt: ago(0)}}}
	be.state["box"] = &fakeHost{dialErr: &client.UserError{Msg: "Box refused the key:\nPermission denied (publickey)", Hint: "ssh box"}}
	hs := newHarness(t, be, 100, 30)
	hs.m.motion = false
	hs.key("i")
	if screen := plainScreen(hs.m); !strings.Contains(screen, "Box refused the key: Permission denied (publickey)") {
		t.Errorf("the details split the status:\n%s", screen)
	}
	hs.key("esc")
	hs.m.openHost(nil)
	screen := plainScreen(hs.m)
	lines := strings.Split(strings.TrimRight(screen, "\n"), "\n")
	if !strings.Contains(screen, "✕ Box refused the key: Permission denied (publickey)") || !strings.Contains(lines[len(lines)-1], "esc") {
		t.Errorf("all sessions split the note, or lost the keys:\n%s", screen)
	}
}
