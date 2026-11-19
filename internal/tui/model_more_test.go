package tui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func TestPalette(t *testing.T) {
	hs := home(t, 80, 24)
	hs.m.openHost(hs.m.hostByName("aws-gpu"))
	hs.key("ctrl+o")
	p, ok := hs.m.modal.(*palette)
	if !ok {
		t.Fatalf("ctrl+o opened %T", hs.m.modal)
	}
	hs.typeText("session new")
	items := p.items(hs.m)
	if len(items) == 0 || items[0].c.name() != "session: new" || items[0].reason != "" {
		t.Fatalf("default lookup was blocked by an unrelated offline host: %+v", items)
	}
	hs.key("enter")
	if hs.m.modal != nil || hs.m.prompt != nil {
		t.Fatal("a missing default on an offline context opened a start")
	}
	hasToast(t, hs.m, "aws-gpu is offline.")

	// Availability and a disabled command's open palette remain checked
	// independently: host-open cannot run while a sessions view is open.
	hs.key("ctrl+o")
	hs.typeText("host open")
	p = hs.m.modal.(*palette)
	items = p.items(hs.m)
	if len(items) == 0 || items[0].c.name() != "host: open" || items[0].reason != "the sessions are open" || !strings.Contains(plainScreen(hs.m), "Open host · the sessions are open") {
		t.Fatalf("the palette lost its disabled-command reason: %+v", items)
	}
	hs.key("enter")
	if hs.m.modal != p {
		t.Fatal("a disabled command closed the palette")
	}
	hasToast(t, hs.m, "Not available: the sessions are open.")
	hs.key("esc", "esc")
	hs.m.hostSel = 0
	hs.key("enter", "ctrl+o")
	hs.typeText("toggle ended")
	hs.key("enter")
	if hs.m.modal != nil || !hs.m.hideExited {
		t.Fatal("the palette did not run the command")
	}
}

func TestHelpComesFromTheRegistry(t *testing.T) {
	for _, open := range []bool{false, true} {
		hs := home(t, 80, 36)
		if open {
			hs.key("enter")
		}
		hs.key("?")
		if _, ok := hs.m.modal.(*helpSheet); !ok {
			t.Fatalf("? opened %T", hs.m.modal)
		}
		out := plainScreen(hs.m)
		for _, c := range commands {
			for _, b := range c.keys {
				// The sheet leaves out its own key: it is open already.
				if !c.hidden && c.applies(hs.m, b) && c.name() != "view: shortcuts" && !strings.Contains(out, keyName(b.key)) {
					t.Errorf("screen %v: the keys miss %s for %s", hs.m.screen, keyName(b.key), c.name())
				}
			}
		}
		hs.key("?")
		if hs.m.modal != nil {
			t.Fatal("? did not close the keys")
		}
	}
}

func TestReconnect(t *testing.T) {
	hs := home(t, 80, 24)
	h := hs.m.hostByName("atlas-dev")
	gen := h.gen
	hs.be.conn("atlas-dev").Close() // the link drops
	hs.do(nil)
	if h.status != stOffline || h.online() {
		t.Fatalf("status %v after the connection closed", h.status)
	}
	if !strings.Contains(plainScreen(hs.m), "atlas-dev") {
		t.Fatal("the host vanished")
	}
	if n := hs.fire(func(m tea.Msg) bool { r, ok := m.(redialMsg); return ok && r.name == "atlas-dev" }); n != 1 {
		t.Fatalf("%d redials scheduled, want 1", n)
	}
	if !h.online() || h.gen == gen || hs.be.dials["atlas-dev"] != 2 {
		t.Fatalf("not reconnected: status %v, dials %d", h.status, hs.be.dials["atlas-dev"])
	}
	if h.backoff != 0 {
		t.Fatal("the backoff was not reset")
	}
}

func TestBackoff(t *testing.T) {
	var got []time.Duration
	d := time.Duration(0)
	for range 7 {
		d = nextBackoff(d)
		got = append(got, d)
	}
	want := []int{1, 2, 4, 8, 16, 30, 30} // seconds
	for i := range want {
		if got[i] != time.Duration(want[i])*time.Second {
			t.Fatalf("backoff %v", got)
		}
	}
}

func TestOfflineHostShowsTry(t *testing.T) {
	hs := home(t, 80, 24)
	hs.key("down", "enter")
	out := plainScreen(hs.m)
	for _, want := range []string{"✕ Cannot reach aws-gpu:", "→ ssh aws-gpu   (check that this works first)", "Retrying in the background."} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	hs.key("n")
	if hs.m.modal != nil {
		t.Fatal("a new-session dialog opened on an offline host")
	}
	hasToast(t, hs.m, "Not available: aws-gpu is offline.")
}

func TestWizard(t *testing.T) {
	be := newFakeBackend()
	be.probe = client.Platform{OS: "linux", Arch: "arm64"}
	be.known = []string{"bastion", "raspberry"}
	hs := newHarness(t, be, 80, 24)
	hs.key("n")
	if hs.m.modal != nil {
		t.Fatal("n did something on the first-run screen")
	}
	hs.key("a")
	w, ok := hs.m.modal.(*wizard)
	if !ok {
		t.Fatalf("a opened %T", hs.m.modal)
	}
	hs.key("down", "down")
	if w.in[0].value() != "raspberry" || w.in[1].value() != "raspberry" {
		t.Fatalf("picking from the SSH config gave %q, %q", w.in[0].value(), w.in[1].value())
	}
	hs.key("tab", "tab")
	hs.typeText("~/infra/ssh/config")
	hs.key("enter")
	if w.step != wzTest || w.err != nil || len(w.checks) != 4 {
		t.Fatalf("test step: %v %v %+v", w.step, w.err, w.checks)
	}
	for i, name := range []string{"host key", "connect", "system", "helper"} {
		if w.checks[i].name != name {
			t.Fatalf("test check %d is %q, want %q", i, w.checks[i].name, name)
		}
	}
	hs.key("enter", "1")
	if w.step != wzInstall {
		t.Fatalf("no consent screen: step %v", w.step)
	}
	hs.key("esc", "esc")
	if w.step != wzTest {
		t.Fatal("esc did not go back")
	}
	hs.key("enter", "1", "enter")
	hasCall(t, be, "install raspberry")
	if !w.saved || w.step != wzReady {
		t.Fatalf("not ready: step %v, err %v", w.step, w.err)
	}
	want := client.Host{Name: "raspberry", SSH: "raspberry", SSHConfig: "~/infra/ssh/config"}
	if len(be.hosts) != 1 || be.hosts[0].Name != want.Name || be.hosts[0].SSHConfig != want.SSHConfig {
		t.Fatalf("saved %+v", be.hosts)
	}
	h := hs.m.hostByName("raspberry")
	if h == nil || !h.online() || be.dials["raspberry"] != 1 {
		t.Fatal("the wizard's connection was not adopted")
	}
	if hs.m.screen != scrSessions || hs.m.cur != "raspberry" {
		t.Fatal("the new host's sessions are not open")
	}
	// The host has no agents yet, so the checklist comes next
	// (guide_test.go follows it to the end).
	hs.key("enter")
	if w.step != wzAgents {
		t.Fatalf("after ready: step %v", w.step)
	}
}

func TestWizardSSHNeedsThePrompt(t *testing.T) {
	be := newFakeBackend()
	be.probeErr = &client.UserError{Msg: "cannot reach box: Permission denied (publickey).", Hint: "ssh box"}
	hs := newHarness(t, be, 80, 24)
	hs.key("a")
	hs.typeText("me@box.example")
	hs.key("tab", "ctrl+u")
	hs.typeText("box")
	hs.key("enter")
	w := hs.m.modal.(*wizard)
	_, hint := errText(w.err)
	if want := brand.Name + " host add box me@box.example"; !strings.HasPrefix(hint, want) {
		t.Fatalf("hint %q, want it to start with %q", hint, want)
	}
}

func TestWizardChecksTheName(t *testing.T) {
	hs := newHarness(t, labBackend(), 80, 24)
	hs.key("a")
	hs.typeText("gcp-dev")
	hs.key("enter")
	w := hs.m.modal.(*wizard)
	if msg, _ := errText(w.err); msg != "there is already a host named gcp-dev" {
		t.Fatalf("error %q", msg)
	}
	if w.step != wzWhere {
		t.Fatal("it went on")
	}
}

func TestRemoveHost(t *testing.T) {
	hs := home(t, 80, 24)
	hs.key("ctrl+o")
	hs.typeText("host remove")
	hs.key("enter", "enter")
	if hs.m.hostByName("atlas-dev") != nil || len(hs.be.hosts) != 2 {
		t.Fatal("the host was not removed")
	}
	hasToast(t, hs.m, "Forgot atlas-dev. Its sessions and the helper stay on the host.")
}

func TestToasts(t *testing.T) {
	var s toastStack
	for _, x := range []string{"a", "b", "c", "d", "e"} {
		s.push(now, toastInfo, x, "")
	}
	if len(s.shown) != 3 || len(s.queue) != 2 {
		t.Fatalf("%d shown, %d queued", len(s.shown), len(s.queue))
	}
	s.push(now.Add(2*time.Second), toastInfo, "a", "")
	s.push(now, toastInfo, "d", "")
	if len(s.queue) != 2 {
		t.Fatal("a repeat was queued")
	}
	s.expire(now.Add(toastLife))
	var texts []string
	for _, x := range s.shown {
		texts = append(texts, x.text)
	}
	if !slices.Equal(texts, []string{"a", "d", "e"}) || len(s.queue) != 0 {
		t.Fatalf("after expiry %q", texts)
	}
	if d, ok := s.next(now.Add(toastLife)); !ok || d != 2*time.Second {
		t.Fatalf("next %v", d)
	}
}

func TestMotion(t *testing.T) {
	env := func(kv ...string) func(string) string {
		return func(k string) string {
			for i := 0; i < len(kv); i += 2 {
				if kv[i] == k {
					return kv[i+1]
				}
			}
			return ""
		}
	}
	cases := []struct {
		setting string
		env     func(string) string
		want    bool
	}{
		{"auto", env(), true},
		{"auto", env("SSH_CONNECTION", "1.2.3.4 5 6.7.8.9 22"), false},
		{"auto", env("NO_COLOR", "1"), false},
		{"auto", env("TERM", "dumb"), false},
		{"auto", env("TERMINAL_EMULATOR", "JetBrains-JediTerm"), false},
		{"on", env("SSH_CONNECTION", "x"), true},
		{"off", env(), false},
	}
	for _, c := range cases {
		if got := motionOn(c.setting, c.env); got != c.want {
			t.Errorf("motionOn(%q) = %v, want %v", c.setting, got, c.want)
		}
	}
}

func TestFramesOnlyWhenSomethingMoves(t *testing.T) {
	be := labBackend()
	hs := newHarness(t, be, 80, 24)
	if hs.m.frameNeed() != 0 {
		t.Fatal("motion off, yet frames tick")
	}
	hs.m.motion = true
	hs.clock = hs.clock.Add(time.Minute) // the list has long since appeared
	if d := hs.m.frameNeed(); d != slowFrame {
		t.Fatalf("working and waiting sessions tick at %v, want %v", d, slowFrame)
	}
	for _, h := range hs.m.hosts {
		for i := range h.sessions {
			h.sessions[i].State = api.StateIdle
		}
	}
	if d := hs.m.frameNeed(); d != 0 {
		t.Fatalf("an idle screen ticks every %v", d)
	}
	hs.key("?") // a sheet rises
	if d := hs.m.frameNeed(); d != fastFrame {
		t.Fatalf("a sheet rising ticks at %v", d)
	}
}

func TestRenderScreen(t *testing.T) {
	// A terminal query in the output must not block the emulator.
	done := make(chan *screenShot, 1)
	go func() { done <- renderScreen([]byte("\x1b[c\x1b[6n\x1b[?2048hhello\r\n\x1b[31mworld\x1b[0m"), 20, 5) }()
	select {
	case s := <-done:
		got := s.view(10, 3)
		if len(got) != 3 || !strings.Contains(got[0], "hello") || !strings.Contains(got[1], "world") {
			t.Fatalf("view %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the emulator blocked")
	}
}

func TestFit(t *testing.T) {
	if got := fit("auth-refactor", 6); got != "auth-…" {
		t.Errorf("fit = %q", got)
	}
	if got := truncPath("/home/dev/src/app/internal/auth", 20); got != "/home/dev/src/…/auth" {
		t.Errorf("truncPath = %q", got)
	}
	if got := fmtMemory(6549825126, 8<<30); got != "6.1 of 8 GiB" {
		t.Errorf("fmtMemory = %q", got)
	}
	if _, ok := fuzzy("sn", "session: new"); !ok {
		t.Error("fuzzy missed")
	}
}

func TestRunNeedsATerminal(t *testing.T) {
	// Under go test, stdin is not a terminal.
	err := Run(context.Background())
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Hint == "" {
		t.Fatalf("Run = %v", err)
	}
}

func TestStaleListIsIgnored(t *testing.T) {
	hs := home(t, 80, 24)
	h := hs.m.hostByName("atlas-dev")
	before := len(h.sessions)
	h.changes++ // a local edit after the list below was asked for
	_, cmd := hs.m.Update(sessionsMsg{name: h.name(), gen: h.gen, changes: h.changes - 1, list: nil})
	if len(h.sessions) != before {
		t.Fatal("a stale list replaced the sessions")
	}
	hs.do(cmd)
	if len(h.sessions) != before || h.listing {
		t.Fatal("the list was not asked for again")
	}
}

func TestMarkMotion(t *testing.T) {
	hs := newHarness(t, newFakeBackend(), 80, 24)
	still := strings.Join(hs.m.markLines(), "\n")
	hs.clock = hs.clock.Add(700 * time.Millisecond)
	if strings.Join(hs.m.markLines(), "\n") != still {
		t.Fatal("the mark moved with motion off")
	}
	spaced := strings.Join(strings.Split(brand.Name, ""), " ")
	if got := ansi.Strip(hs.m.welcomeTitle()); got != spaced {
		t.Fatalf("with motion off the title is whole at once, got %q", got)
	}
	hs.m.motion = true
	hs.m.epoch = hs.clock
	if got := ansi.Strip(hs.m.welcomeTitle()); got == spaced {
		t.Fatalf("with motion on the title decodes, got %q at once", got)
	}
	a := strings.Join(hs.m.markLines(), "\n")
	hs.clock = hs.clock.Add(800 * time.Millisecond)
	if strings.Join(hs.m.markLines(), "\n") == a {
		t.Fatal("the mark did not move with motion on")
	}
	if got := ansi.Strip(hs.m.welcomeTitle()); got != spaced {
		t.Fatalf("the title did not finish decoding in 800 ms: %q", got)
	}
}

func TestFolderCompletion(t *testing.T) {
	hs := home(t, 80, 24)
	hs.key("n", "ctrl+f", "tab")
	d := hs.m.modal.(*newSession)
	if !d.hints || len(d.suggest) == 0 || d.suggest[0] != "~/src/app" {
		t.Fatalf("an empty folder should show recent folders as hints, got %v (hints %v)", d.suggest, d.hints)
	}
	hs.key("tab") // hints are not taken by Tab: it moves on
	if d.field != 2 {
		t.Fatalf("tab with only hints should move to the name field, field %d", d.field)
	}
	hs.key("shift+tab", "ctrl+u")
	hs.typeText("~/src/a")
	if d.hints || len(d.suggest) != 2 {
		t.Fatalf("typing should list matches, got %v", d.suggest)
	}
	hs.key("tab")
	if got := d.folder.value(); got != "~/src/api/" {
		t.Fatalf("first tab completed to %q", got)
	}
	if d.field != 1 {
		t.Fatal("a completion should stay in the folder field")
	}
}

func TestOtherCommand(t *testing.T) {
	hs := home(t, 80, 24)
	hs.key("n", "ctrl+f")
	d := hs.m.modal.(*newSession)
	for d.agent() != otherAgent {
		hs.key("right")
	}
	hs.key("enter") // no command yet: an error, and focus on the command field
	if d.err == nil || d.field != 4 {
		t.Fatalf("starting without a command should ask for one (err %v, field %d)", d.err, d.field)
	}
	hs.typeText("aider --model sonnet")
	hs.key("tab") // command -> folder
	if d.field != 1 {
		t.Fatalf("tab from the command should go to the folder, field %d", d.field)
	}
	hs.key("enter")
	hasCall(t, hs.be, "new atlas-dev agent= argv=[/bin/sh -c exec aider --model sonnet] cwd=~ name= worktree=false size=80x24")
}

func TestPreviewOpensOnAKey(t *testing.T) {
	hs := sessions(t, 80, 24)
	if strings.Contains(plainScreen(hs.m), "Welcome back") {
		t.Fatal("a preview shows before it is asked for")
	}
	hs.key("space")
	if !hs.m.previewOpen() || hs.m.preview.shot == nil || hs.m.preview.shot.blank {
		t.Fatal("space did not load the preview")
	}
	if !strings.Contains(plainScreen(hs.m), "Welcome back") {
		t.Fatal("the preview does not show the last screen")
	}
	hs.key("esc")
	if hs.m.modal != nil {
		t.Fatal("esc did not close the preview")
	}
	hs.key("space", "enter")
	if len(hs.be.attached) != 1 || hs.be.attached[0].Session != "s4" {
		t.Fatal("enter in the preview did not attach")
	}
}

func TestKeyNames(t *testing.T) {
	for k, want := range map[string]string{"enter": "enter", "ctrl+o": "ctrl+o", "alt+e": optKey + "+e", "n": "n", "?": "?", "f2": "f2", "space": "space"} {
		if got := keyName(k); got != want {
			t.Errorf("keyName(%q) = %q, want %q", k, got, want)
		}
	}
}
