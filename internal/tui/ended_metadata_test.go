package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/proto"
)

// Hostile terminal sequences in a session's name, folder, agent or ended
// signal never reach the delete sheet, its toast or the notice after an
// attach, while DeleteNamed still receives the exact raw stored name so the
// host's guarded delete matches it (decision 10, addendum A2).

func TestDeleteSheetAndAttachDoneCleanHostileMetadata(t *testing.T) {
	const (
		rawName = "auth\x1bc\x1b]52;c;YWJj\x07\u202efix"
		rawSig  = "killed\x1bc\x1b]52;c;YWJj\x07\u202e"
	)
	checkClean := func(t *testing.T, where, s string) {
		t.Helper()
		for _, bad := range []string{"\x1bc", "\x1b]52", "\x07", "\u202e"} {
			if strings.Contains(s, bad) {
				t.Errorf("%s leaked %q in %q", where, bad, s)
			}
		}
	}

	be := labBackend()
	be.state["atlas-dev"].sessions = []client.Session{{
		ID: "s1", Name: rawName, Agent: "claude\x1bc\u202e", Cwd: "/srv/app\x1bc\u202e",
		State: api.StateWorking, Alive: true, Since: ago(time.Minute), CreatedAt: ago(time.Hour),
	}}
	hs := newHarness(t, be, 90, 24)
	hs.m.motion = false
	hs.key("enter")
	checkClean(t, "sessions view", hs.m.View().Content)

	hs.key("d")
	cf, ok := hs.m.modal.(*confirm)
	if !ok {
		t.Fatalf("d opened %T, want *confirm", hs.m.modal)
	}
	if strings.Contains(cf.title, "\x1b") || strings.Contains(strings.Join(cf.lines, "\n"), "\x1b") {
		t.Errorf("delete sheet modal kept escape runes: title=%q lines=%q", cf.title, cf.lines)
	}
	checkClean(t, "delete sheet raw view", hs.m.View().Content)
	if plain := plainScreen(hs.m); !strings.Contains(plain, "Delete authc]52;c;YWJjfix?") {
		t.Errorf("delete sheet title not cleaned:\n%s", plain)
	}

	// Confirming the delete sends the exact raw s.Name to fakeConn.DeleteNamed,
	// which rejects any cleaned or altered name with session_renamed.
	hs.key("enter")
	if left := be.state["atlas-dev"].sessions; len(left) != 0 {
		t.Fatalf("guarded delete did not pass exact raw session name; remaining sessions: %+v, toasts: %+v", left, hs.m.toasts.shown)
	}
	checkClean(t, "post-delete view", hs.m.View().Content)
	hasToast(t, hs.m, "Deleted authc]52;c;YWJjfix on atlas-dev.")

	hs.m.onAttachDone(attachDoneMsg{
		host: "atlas\x1bc\u202edev",
		id:   "s1",
		name: rawName,
		res:  client.AttachResult{Ended: &proto.Ended{ExitCode: 137, Signal: rawSig}},
	})
	checkClean(t, "post-attach view", hs.m.View().Content)
	hasToast(t, hs.m, "authc]52;c;YWJjfix on atlascdev ended (killedc]52;c;YWJj).")
}

// Inline rename ("r") and folder completions in quick-start ("n") and the
// new-session sheet ("ctrl+f") strip terminal escapes and bidi controls before
// entering or rendering an input field, and pressing Enter without editing a
// cleaned hostile name cancels without mutating the stored name (decision 11,
// item 3).
func TestRenameAndFolderCompletionCleanHostileMetadata(t *testing.T) {
	const (
		rawName   = "auth\x1bc\x1b]52;c;YWJj\x07\u202efix"
		rawFolder = "~/src/app\x1bc\x1b]52;c;YWJj\x07\u202e"
	)
	checkClean := func(t *testing.T, where, s string) {
		t.Helper()
		for _, bad := range []string{"\x1bc", "\x1b]52", "\x07", "\u202e"} {
			if strings.Contains(s, bad) {
				t.Errorf("%s leaked %q in %q", where, bad, s)
			}
		}
	}

	be := labBackend()
	be.state["atlas-dev"].sessions = []client.Session{{
		ID: "s1", Name: rawName, Agent: "claude", Cwd: "/srv/app",
		State: api.StateWorking, Alive: true, Since: ago(time.Minute), CreatedAt: ago(time.Hour),
	}}
	hs := newHarness(t, be, 90, 24)
	hs.m.motion = false
	hs.key("enter")

	// Inline rename cleans the initial value, and unedited Enter cancels.
	hs.key("r")
	if hs.m.rename == nil {
		t.Fatal("r did not start inline rename")
	}
	checkClean(t, "inline rename view", hs.m.View().Content)
	hs.key("enter")
	if hs.m.rename != nil {
		t.Fatal("unedited Enter did not close inline rename")
	}
	if got := be.state["atlas-dev"].sessions[0].Name; got != rawName {
		t.Fatalf("unedited Enter mutated session name to %q, want %q", got, rawName)
	}

	// Editing and submitting cleans the old name in the confirmation toast.
	hs.key("r")
	hs.key("ctrl+u")
	hs.typeText("clean")
	hs.key("enter")
	checkClean(t, "post-rename view", hs.m.View().Content)
	hasToast(t, hs.m, "Renamed authc]52;c;YWJjfix to clean on atlas-dev.")

	// Quick-start folder ghost and Tab completion clean host-supplied paths.
	const wantFolder = "~/src/appc]52;c;YWJj"
	hs.key("n")
	hs.typeText("~/sr")
	hs.m.onFolders(foldersMsg{
		host:   "atlas-dev",
		prefix: "~/sr",
		quick:  true,
		res:    client.FoldersResult{Matches: []string{rawFolder}},
	})
	if len(hs.m.prompt.folders) != 1 || hs.m.prompt.folders[0] != wantFolder {
		t.Fatalf("quick-start folders = %#v, want [%q]", hs.m.prompt.folders, wantFolder)
	}
	checkClean(t, "quick-start ghost view", hs.m.View().Content)
	hs.key("tab")
	if got := hs.m.prompt.in.value(); got != wantFolder {
		t.Fatalf("quick-start completed input = %q, want %q", got, wantFolder)
	}
	checkClean(t, "quick-start completed view", hs.m.View().Content)
	hs.key("esc")

	// New-session sheet folder suggestions and Tab completion clean paths too.
	hs.key("n", "ctrl+f", "down")
	d, ok := hs.m.modal.(*newSession)
	if !ok || d.field != 1 {
		t.Fatalf("new-session folder field not focused: modal=%T field=%v", hs.m.modal, d)
	}
	hs.key("ctrl+u")
	hs.typeText("~/sr")
	hs.m.onFolders(foldersMsg{
		host:   "atlas-dev",
		prefix: "~/sr",
		res:    client.FoldersResult{Matches: []string{rawFolder}},
	})
	if len(d.suggest) != 1 || d.suggest[0] != wantFolder {
		t.Fatalf("new-session suggestions = %#v, want [%q]", d.suggest, wantFolder)
	}
	checkClean(t, "new-session suggestion view", hs.m.View().Content)
	hs.key("tab")
	if got := d.folder.value(); got != wantFolder {
		t.Fatalf("new-session completed folder = %q, want %q", got, wantFolder)
	}
	checkClean(t, "new-session completed view", hs.m.View().Content)
}
