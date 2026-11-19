package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// hostile is text that a host could send: an OSC 52 write to the clipboard,
// a bidirectional override, a zero-width space and a tag character.
const hostile = "\x1b]52;c;aGk=\x07Allow\u202e this\u200b?\U000e0041"

// hiddenBits lists what must never reach the screen from a host.
var hiddenBits = []string{"\x1b]52", "\x1b]0", "\x07", "\x1b[2J", "\u202e", "\u200b", "\u200c", "\U000e0041", "\u009b"}

func checkClean(t *testing.T, what, raw string) {
	t.Helper()
	for _, b := range hiddenBits {
		if strings.Contains(raw, b) {
			t.Errorf("%s holds %q from the host:\n%q", what, b, raw)
		}
	}
}

// TestHostTextDrawsAsPlainText checks that what a host sends reaches the
// screen as plain text: a reason that would write to the clipboard, a name
// that would clear the screen, and a folder, an agent, a system and a
// version with hidden characters (docs/contributing/isolation-spec.md 5.5).
// The model keeps the host's own values, for the requests that go back.
func TestHostTextDrawsAsPlainText(t *testing.T) {
	be := labBackend()
	h := be.state["atlas-dev"]
	s4 := &h.sessions[3]
	s4.Reason = hostile
	s4.Name = "auth\u200b-refactor\x1b[2J"
	s4.Cwd = "/home/dev/src/app\U000e0041\x07"
	s4.Agent = "claude\u009b2J"
	h.info.Distro = "Debian\x1b]0;owned\x07 13"
	h.info.AgentVersions["claude"] = "2.1.3\u202e"

	hs := newHarness(t, be, 120, 36)
	hs.m.motion = false
	hs.key("i")
	checkClean(t, "the host details", hs.m.render())
	if out := plainScreen(hs.m); !strings.Contains(out, "Debian]0;owned 13") || !strings.Contains(out, "claude 2.1.3") {
		t.Errorf("the host details lost the host's text:\n%s", out)
	}

	hs.key("esc", "enter")
	s := hs.m.selected()
	if s == nil || s.ID != "s4" {
		t.Fatalf("selected %v, want s4", s)
	}
	if s.Name != "auth\u200b-refactor\x1b[2J" || s.Cwd != "/home/dev/src/app\U000e0041\x07" {
		t.Errorf("the model changed the host's own values: %+v", *s)
	}
	checkClean(t, "the sessions", hs.m.render())
	out := plainScreen(hs.m)
	for _, want := range []string{"● ]52;c;aGk=Allow this?", "auth-refactor[2J", "~/src/app", "claude2J"} {
		if !strings.Contains(out, want) {
			t.Errorf("the sessions do not show %q:\n%s", want, out)
		}
	}
	hs.key("d")
	checkClean(t, "the delete question", hs.m.render())
	hs.key("esc")
	// The search matches what the rows show.
	if !matchSession("auth-refactor", *s) {
		t.Error("a search for the name shown does not find the session")
	}
}

// TestPreviewCleansItsCells checks the peek and the activity line: the
// session's own screen, whose terminal model keeps tag characters.
func TestPreviewCleansItsCells(t *testing.T) {
	screen := []byte("\x1b]8;id=a\u202e;https://evil.example/\u200bx\x1b\\link\x1b]8;;\x1b\\\r\n" +
		"a\u202eb\u200bc\u009b2Jd\U000e0041e\u2066f\r\nWorking on it\U000e0041 now\r\n")
	shot := renderScreen(screen, 30, 4)
	view := strings.Join(shot.view(30, 4), "\n")
	checkClean(t, "the peek", view)
	if !strings.Contains(view, "def") || !strings.Contains(view, "link") {
		t.Errorf("the peek lost the text: %q", view)
	}
	// An OSC 8 link's address and parameters are the session's text: the
	// peek shows the link's text, and no link.
	if strings.Contains(view, "\x1b]8") || strings.Contains(view, "evil.example") {
		t.Errorf("the peek keeps the host's link: %q", view)
	}
	if got := activityLine(shot); got != "Working on it now" {
		t.Errorf("activity %q", got)
	}
}

// TestHostNotificationIsClean checks that a desktop notification carries a
// host's reason as plain text, and cannot end early.
func TestHostNotificationIsClean(t *testing.T) {
	hs := sessions(t, 80, 24)
	hs.send(tea.BlurMsg{})
	h := hs.be.state["atlas-dev"]
	hs.be.mu.Lock()
	h.sessions[2].State, h.sessions[2].Reason = api.StateNeedsInput, "ok\x1b\\"+hostile
	hs.be.mu.Unlock()
	hs.fire(func(m tea.Msg) bool { _, ok := m.(refreshTickMsg); return ok })
	var notes []string
	for _, r := range hs.raw {
		if strings.Contains(r, "\x1b]777;notify;") {
			notes = append(notes, r)
		}
	}
	if len(notes) != 1 || !strings.HasSuffix(notes[0], ";api-tests needs you: ok\\]52 c aGk=Allow this?\x1b\\") {
		t.Fatalf("notifications %q", notes)
	}
	checkClean(t, "the notification", strings.TrimPrefix(notes[0], "\x1b]777;notify;"))
}

// TestRememberedSessionsDrawClean checks that sessions from the launcher's
// memory, which hold what the host sent, draw clean before any host
// answers.
func TestRememberedSessionsDrawClean(t *testing.T) {
	be := labBackend()
	list := append([]client.Session(nil), be.state["gcp-dev"].sessions...)
	list[0].Name = "bench\x1b[2J"
	list[0].State, list[0].Reason = api.StateNeedsInput, hostile
	be.mem = memory{
		Sessions: map[string][]client.Session{"gcp-dev": list},
		Last:     &lastSession{Host: "gcp-dev", ID: "g6", Name: "bench"},
	}
	m := New(context.Background(), be, Options{Now: func() time.Time { return now }})
	m.width, m.height = 80, 24
	checkClean(t, "the remembered sessions", m.render())
	if s := m.selected(); s == nil || s.Name != "bench\x1b[2J" {
		t.Fatalf("selected %+v, want bench as the host sent it", s)
	}
}

// TestFolderCompletionNeverPicksAnotherFolder checks a suggestion that
// cleaning would change: it would draw as ~/src/mind/, and a start there
// would go to another folder, or make it. The launcher does not offer it.
func TestFolderCompletionNeverPicksAnotherFolder(t *testing.T) {
	be := labBackend()
	be.state["atlas-dev"].folders = []string{"~/src/mi\u200cnd/"}
	hs := newHarness(t, be, 80, 24)
	hs.m.motion = false
	hs.key("n")
	hs.typeText("claude ~/src/mi")
	hs.key("tab", "enter")
	var starts []string
	for _, c := range be.callList() {
		if strings.HasPrefix(c, "new atlas-dev agent=") {
			starts = append(starts, c)
		}
	}
	if len(starts) != 1 || !strings.Contains(starts[0], "cwd=~/src/mi ") {
		t.Fatalf("starts %q, want one in the folder typed, ~/src/mi", starts)
	}
}

// TestHiddenSecretNameIsRefused checks a resume of a session whose host
// names a secret with a hidden character: the launcher refuses it, as the
// command line does, and fetches nothing.
func TestHiddenSecretNameIsRefused(t *testing.T) {
	be := labBackend()
	s1 := &be.state["atlas-dev"].sessions[0] // docs-pass, which has ended
	s1.Secrets = []string{"GH_TOKEN\u200b"}
	hs := newHarness(t, be, 80, 24)
	hs.m.motion = false
	hs.key("enter")
	hs.m.sel, hs.m.autoSel = sessKey{"atlas-dev", "s5"}, false
	hs.key(resumeKey)
	for _, c := range be.callList() {
		if strings.HasPrefix(c, "secrets") || strings.HasPrefix(c, "resume") {
			t.Fatalf("the resume went on: %q", be.callList())
		}
	}
	if out := plainScreen(hs.m); !strings.Contains(out, "is not a name.") {
		t.Fatalf("no refusal:\n%s", out)
	}
	checkClean(t, "the refusal", hs.m.render())
}

// TestQuestionsShowHostPaths checks the setup question and the make-folder
// question: the host's paths show cleaned, or quoted when cleaning would
// change them. The answer is kept for the repository's own path.
func TestQuestionsShowHostPaths(t *testing.T) {
	const repo = "/home/dev/src/app\x1b]52;c;aGk=\x07\u202e"
	hs := trustLab(t)
	hs.be.state["atlas-dev"].setupRepo = repo
	hs.key("n")
	hs.typeText("claude ~/src/app")
	hs.key("enter")
	if _, ok := hs.m.modal.(*setupAsk); !ok {
		t.Fatalf("no question; modal %T", hs.m.modal)
	}
	checkClean(t, "the setup question", hs.m.render())
	if out := plainScreen(hs.m); !strings.Contains(out, `"~/src/app\x1b]52;c;aGk=\a\u202e"`) {
		t.Errorf("the setup question does not quote the repository:\n%s", out)
	}
	hs.key("y")
	hasCall(t, hs.be, "remember atlas-dev "+repo+" run=true")

	hs = folderLab(t)
	hs.key("n")
	hs.typeText("claude ~/src/new-app")
	hs.key("enter")
	ask, ok := hs.m.modal.(*makeFolderAsk)
	if !ok {
		t.Fatalf("no question; modal %T", hs.m.modal)
	}
	ask.q.Folder = "/home/dev/src/mi\u200cnd"
	checkClean(t, "the make-folder question", hs.m.render())
	if out := plainScreen(hs.m); !strings.Contains(out, `"~/src/mi\u200cnd"`) {
		t.Errorf("the make-folder question does not quote the folder:\n%s", out)
	}
}

// TestRenameKeepsAHiddenName checks rename on a name with a hidden
// character: the field shows it cleaned, and Enter on it untouched renames
// nothing. An edit to the clean spelling is the user's own name, and
// renames.
func TestRenameKeepsAHiddenName(t *testing.T) {
	be := labBackend()
	be.state["atlas-dev"].sessions[3].Name = "auth\u200b-refactor"
	hs := newHarness(t, be, 80, 24)
	hs.m.motion = false
	hs.key("enter", "r")
	checkClean(t, "the rename field", hs.m.render())
	hs.key("enter")
	for _, c := range be.callList() {
		if strings.HasPrefix(c, "rename") {
			t.Fatalf("Enter on the untouched name renamed it: %q", c)
		}
	}
	hs.key("r", "backspace")
	hs.typeText("r")
	hs.key("enter")
	hasCall(t, be, "rename atlas-dev s4 auth-refactor")
}

// TestCopiedCommandIsInert checks the attach command that the launcher
// copies, for a session whose name is shell syntax: it names the session
// by its ID.
func TestCopiedCommandIsInert(t *testing.T) {
	be := labBackend()
	be.state["atlas-dev"].sessions[3].Name = "$(touch owned)`id`;a && b | c\nd"
	hs := newHarness(t, be, 80, 24)
	hs.m.motion = false
	hs.key("enter")
	_, cmd := hs.m.Update(keyMsg("y"))
	hs.do(cmd)
	hasToast(t, hs.m, "Copied: "+brand.Name+" attach atlas-dev/s4.")
}

// TestRememberedListDropsInvalidIDs checks a list from the launcher's
// memory with a row whose ID is not a session ID: the row is left out, and
// a remembered selection of it is cleared.
func TestRememberedListDropsInvalidIDs(t *testing.T) {
	be := labBackend()
	list := append([]client.Session(nil), be.state["gcp-dev"].sessions...)
	list = append(list, client.Session{ID: "bad\x1b]52;c;aGk=\x07", Name: "ghost", Agent: "shell", State: api.StateIdle})
	be.mem = memory{
		Sessions: map[string][]client.Session{"gcp-dev": list},
		Last:     &lastSession{Host: "gcp-dev", ID: "bad\x1b]52;c;aGk=\x07", Name: "ghost"},
	}
	m := New(context.Background(), be, Options{Now: func() time.Time { return now }})
	h := m.hostByName("gcp-dev")
	if len(h.sessions) != 2 {
		t.Fatalf("restored %d rows, want the 2 with session IDs", len(h.sessions))
	}
	if m.mem.Last != nil || m.want != (sessKey{}) || m.screen == scrSessions {
		t.Fatalf("the remembered selection of an invalid ID stayed: last %+v, want %v, screen %v", m.mem.Last, m.want, m.screen)
	}
}

// TestCleanConnCleansWhatIsOnlyDrawn checks a connection's answers: the
// session rows stay as the host sent them, the doctor report and the agent
// checks are cleaned, and a folder that cleaning would change is dropped.
func TestCleanConnCleansWhatIsOnlyDrawn(t *testing.T) {
	be := labBackend()
	h := be.state["atlas-dev"]
	h.sessions[0].Reason = hostile
	h.info.Checks = []api.Check{{Name: "linger\x07", Detail: hostile, Fix: "loginctl\u202e"}}
	h.checks = []client.AgentCheck{{Kind: "claude", Title: "Claude\x1b[2J", Path: "/usr/bin/claude\u200b", LoginHow: hostile}}
	h.folders = []string{"~/src/mi\u200cnd/"}
	ctx := context.Background()
	c, err := dialClean(ctx, be, client.Host{Name: "atlas-dev"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	list, err := c.Sessions(ctx)
	if err != nil || list[0].Reason != hostile {
		t.Fatalf("Sessions = %v, %v, want the host's own rows", list, err)
	}
	info, err := c.Doctor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	checks, err := c.AgentChecks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	folders, err := c.Folders(ctx, "~/src/")
	if err != nil {
		t.Fatal(err)
	}
	for what, s := range map[string]string{
		"a check":        info.Checks[0].Name + info.Checks[0].Detail + info.Checks[0].Fix,
		"an agent check": checks[0].Title + checks[0].Path + checks[0].LoginHow,
		"the folders":    strings.Join(folders.Matches, " "),
	} {
		checkClean(t, what, s)
	}
	if len(folders.Matches) != 2 {
		t.Errorf("folders %q, want ~/src/api/ and ~/src/app/", folders.Matches)
	}
	if _, ok := unclean(c).(*fakeConn); !ok {
		t.Errorf("unclean gave %T, want the connection under it", unclean(c))
	}
}

// TestRenameByPaste checks that a paste into the rename field is an edit:
// Enter then renames.
func TestRenameByPaste(t *testing.T) {
	be := labBackend()
	hs := newHarness(t, be, 80, 24)
	hs.m.motion = false
	hs.key("enter", "r")
	hs.send(tea.PasteMsg{Content: "-2"})
	hs.key("enter")
	hasCall(t, be, "rename atlas-dev s4 auth-refactor-2")
}
