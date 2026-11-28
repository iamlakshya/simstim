package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

func TestNewSessionFormWallRowAndDowngradeSheet(t *testing.T) {
	be := labBackend()
	hs := newHarness(t, be, 90, 28)

	// Open the new-session form on atlas-dev (WallFloor: process).
	hs.key("enter", "n", "ctrl+f")
	d, ok := hs.m.modal.(*newSession)
	if !ok {
		t.Fatalf("modal = %T, want *newSession", hs.m.modal)
	}
	view := ansi.Strip(hs.m.View().Content)
	if !strings.Contains(view, "Wall") || !strings.Contains(view, "auto (process)") || !strings.Contains(view, "none…") {
		t.Fatalf("new-session form missing Wall row:\n%s", view)
	}

	// Tab to Folder (1), Name (2), Worktree (3), Wall (5).
	hs.key("tab", "tab", "tab", "tab")
	if d.field != 5 {
		t.Fatalf("field = %d, want 5 (Wall)", d.field)
	}

	// Cycle right through process -> gvisor -> microvm -> none.
	hs.key("right", "right", "right", "right")
	if d.wallChoice() != api.LevelNone {
		t.Fatalf("wallChoice = %q, want none", d.wallChoice())
	}

	// Submitting none opens the red downgrade confirmation sheet.
	hs.key("ctrl+enter")
	ask, ok := hs.m.modal.(*downgradeAsk)
	if !ok {
		t.Fatalf("modal after submitting none = %T, want *downgradeAsk", hs.m.modal)
	}
	askView := ansi.Strip(hs.m.View().Content)
	for _, want := range []string{"Run below the floor of atlas-dev?", "wall floor process", "Type \"none\" to confirm:"} {
		if !strings.Contains(askView, want) {
			t.Errorf("downgrade sheet missing %q:\n%s", want, askView)
		}
	}

	// Pressing Enter without typing the level name refuses and stays on the sheet.
	hs.key("enter")
	if _, still := hs.m.modal.(*downgradeAsk); !still || !ask.mismatch {
		t.Fatalf("empty Enter did not reject: modal=%T mismatch=%v", hs.m.modal, ask.mismatch)
	}

	// Esc cancels back to the new-session form without starting anything.
	before := len(be.state["atlas-dev"].sessions)
	hs.key("esc")
	if _, back := hs.m.modal.(*newSession); !back {
		t.Fatalf("Esc modal = %T, want *newSession", hs.m.modal)
	}
	if len(be.state["atlas-dev"].sessions) != before {
		t.Fatal("cancelled downgrade still started a session")
	}

	// Submit again, type "none", press Enter -> starts downgraded session.
	hs.key("ctrl+enter")
	if _, ok := hs.m.modal.(*downgradeAsk); !ok {
		t.Fatalf("re-submit modal = %T, want *downgradeAsk", hs.m.modal)
	}
	hs.typeText("none")
	hs.key("enter")
	if hs.m.modal != nil {
		t.Fatalf("modal remained open after confirming downgrade: %T", hs.m.modal)
	}
	if len(be.state["atlas-dev"].sessions) != before+1 {
		t.Fatalf("sessions = %d, want %d", len(be.state["atlas-dev"].sessions), before+1)
	}
}

func TestQuickStartOnNoneOnlyAndSharedHostsOpensDowngradeSheet(t *testing.T) {
	t.Run("none-only host", func(t *testing.T) {
		be := newFakeBackend()
		be.hosts = []client.Host{{Name: "dock", SSH: "dock", WallFloor: api.LevelNone}}
		be.state["dock"] = &fakeHost{
			info: client.HostInfo{Agents: map[string]string{"shell": "/bin/sh"}},
		}
		hs := newHarness(t, be, 90, 28)

		hs.key("n", "ctrl+enter")
		ask, ok := hs.m.modal.(*downgradeAsk)
		if !ok || ask.info.want != api.LevelNone || ask.info.from != "default" {
			t.Fatalf("none-only modal = %T (%+v), want *downgradeAsk(none/default)", hs.m.modal, ask)
		}
		hs.typeText("none")
		hs.key("enter")
		if hs.m.modal != nil || len(be.state["dock"].sessions) != 1 {
			t.Fatalf("after confirm: modal=%T sessions=%d", hs.m.modal, len(be.state["dock"].sessions))
		}
	})

	t.Run("shared host with process floor", func(t *testing.T) {
		be := newFakeBackend()
		be.hosts = []client.Host{{
			Name:      "e2b-box",
			SSH:       "e2b-box",
			WallFloor: api.LevelProcess,
			Machine:   &client.Machine{Provider: "e2b", ID: "sbx1"},
		}}
		be.state["e2b-box"] = &fakeHost{
			info: client.HostInfo{Agents: map[string]string{"shell": "/bin/sh"}},
		}
		hs := newHarness(t, be, 90, 28)

		hs.key("n", "ctrl+enter")
		ask, ok := hs.m.modal.(*downgradeAsk)
		if !ok || !ask.info.shared || ask.info.want != api.LevelProcess || ask.info.from != api.LevelGVisor {
			t.Fatalf("shared host modal = %T (%+v), want *downgradeAsk(shared, process/gvisor)", hs.m.modal, ask)
		}
		hs.typeText("process")
		hs.key("enter")
		if hs.m.modal != nil || len(be.state["e2b-box"].sessions) != 1 {
			t.Fatalf("after confirm: modal=%T sessions=%d", hs.m.modal, len(be.state["e2b-box"].sessions))
		}
	})
}

func TestUnpinnedHostNoticeShownOnce(t *testing.T) {
	be := newFakeBackend()
	be.hosts = []client.Host{{Name: "legacy", SSH: "legacy"}}
	be.state["legacy"] = &fakeHost{
		probedFloor: api.LevelProcess,
		info:        client.HostInfo{Agents: map[string]string{"shell": "/bin/sh"}},
	}
	hs := newHarness(t, be, 100, 28)

	hs.key("n", "ctrl+enter")
	if !hs.m.unpinnedNoted["legacy"] {
		t.Fatal("expected unpinnedNoted[legacy] to be true after first start")
	}
	foundNotice := false
	for _, ts := range append(append([]toast(nil), hs.m.toasts.shown...), hs.m.toasts.queue...) {
		if strings.Contains(ts.text, "legacy has no wall floor yet") {
			foundNotice = true
		}
	}
	if !foundNotice {
		t.Fatalf("expected unpinned host toast, got shown=%+v queue=%+v", hs.m.toasts.shown, hs.m.toasts.queue)
	}

	// Second start should not push a duplicate unpinned notice.
	hs.m.toasts.shown, hs.m.toasts.queue = nil, nil
	hs.key("n", "ctrl+enter")
	for _, ts := range append(append([]toast(nil), hs.m.toasts.shown...), hs.m.toasts.queue...) {
		if strings.Contains(ts.text, "legacy has no wall floor yet") {
			t.Fatalf("unpinned notice repeated on second start: %+v", hs.m.toasts)
		}
	}
}

func TestDowngradedSessionRowAndDetailShowBang(t *testing.T) {
	be := newFakeBackend()
	be.hosts = []client.Host{{Name: "devbox", SSH: "devbox", WallFloor: api.LevelProcess}}
	be.state["devbox"] = &fakeHost{
		info: client.HostInfo{Agents: map[string]string{"claude": "/usr/bin/claude", "shell": "/bin/sh"}},
		sessions: []client.Session{
			{
				ID: "s1", Name: "login-claude", Agent: "shell", Cwd: "/home/dev",
				State: api.StateWorking, Alive: true, Since: ago(time.Minute), CreatedAt: ago(2 * time.Minute),
				Wall: &api.WallInfo{Level: api.LevelNone, Driver: api.LevelNone, Floor: api.LevelProcess, DowngradedFrom: api.LevelProcess},
			},
		},
	}
	hs := newHarness(t, be, 110, 28)
	view := ansi.Strip(hs.m.View().Content)
	if !strings.Contains(view, "! login-claude") {
		t.Errorf("session row missing ! badge:\n%s", view)
	}
	if !strings.Contains(view, "! none, below the floor process") {
		t.Errorf("session detail missing downgrade note:\n%s", view)
	}
}

func TestDowngradedSessionSanitizesWallStringsInDetailBar(t *testing.T) {
	const osc52 = "\x1b]52;c;aGk=\x07"
	be := newFakeBackend()
	be.hosts = []client.Host{{Name: "devbox", SSH: "devbox", WallFloor: api.LevelProcess}}
	be.state["devbox"] = &fakeHost{
		info: client.HostInfo{Agents: map[string]string{"shell": "/bin/sh"}},
		sessions: []client.Session{
			{
				ID: "s1", Name: "evil", Agent: "shell", Cwd: "/home/dev",
				State: api.StateWorking, Alive: true, Since: ago(time.Minute), CreatedAt: ago(2 * time.Minute),
				Wall: &api.WallInfo{
					Level:          "none" + osc52 + "\u202e",
					Driver:         "none",
					Floor:          "process" + osc52,
					DowngradedFrom: "process" + osc52 + "\u202e",
				},
			},
		},
	}
	hs := newHarness(t, be, 120, 28)
	raw := hs.m.View().Content
	if strings.Contains(raw, "\x1b]52") || strings.ContainsRune(raw, '\u202e') {
		t.Fatalf("rendered TUI leaked raw OSC 52 or bidi override from Session.Wall: %q", raw)
	}
}

func TestOpenShellOnUnwalledSessionPropagatesDowngrade(t *testing.T) {
	be := newFakeBackend()
	be.hosts = []client.Host{{Name: "devbox", SSH: "devbox", WallFloor: api.LevelProcess}}
	be.state["devbox"] = &fakeHost{
		info: client.HostInfo{Agents: map[string]string{"shell": "/bin/sh"}},
		sessions: []client.Session{
			{
				ID: "s1", Name: "login-claude", Agent: "shell", Cwd: "/home/dev/app",
				State: api.StateWorking, Alive: true, Since: ago(time.Minute), CreatedAt: ago(2 * time.Minute),
				Wall: &api.WallInfo{Level: api.LevelNone, Driver: api.LevelNone, Floor: api.LevelProcess, DowngradedFrom: api.LevelProcess},
			},
		},
	}
	hs := newHarness(t, be, 110, 28)
	hs.key("t")
	sessions := be.state["devbox"].sessions
	if len(sessions) != 2 {
		t.Fatalf("sessions after t on unwalled session = %d, want 2", len(sessions))
	}
	sh := sessions[1]
	if sh.Wall == nil || sh.Wall.Level != api.LevelNone || sh.Wall.Floor != api.LevelProcess || sh.Wall.DowngradedFrom != api.LevelProcess {
		t.Fatalf("shell session Wall = %+v, want level=none floor=process downgraded_from=process", sh.Wall)
	}
}
