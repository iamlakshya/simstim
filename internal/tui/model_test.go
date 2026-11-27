package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/proto"
)

// toastTexts lists the toasts shown and queued.
func toastTexts(m *Model) []string {
	var out []string
	for _, t := range append(append([]toast(nil), m.toasts.shown...), m.toasts.queue...) {
		out = append(out, t.text)
	}
	return out
}

func hasToast(t *testing.T, m *Model, want string) {
	t.Helper()
	if !slices.Contains(toastTexts(m), want) {
		t.Fatalf("no toast %q; have %q", want, toastTexts(m))
	}
}

func hasCall(t *testing.T, be *fakeBackend, want string) {
	t.Helper()
	if !slices.Contains(be.callList(), want) {
		t.Fatalf("no call %q; have %q", want, be.callList())
	}
}

func TestSessionOrder(t *testing.T) {
	hs := sessions(t, 80, 24)
	var names []string
	for _, r := range hs.m.rows() {
		names = append(names, r.s.Name)
	}
	want := []string{"auth-refactor", "api-tests", "notes", "docs-pass"}
	if !slices.Equal(names, want) {
		t.Fatalf("order %q, want %q", names, want)
	}
}

func TestConnectsAndShowsHosts(t *testing.T) {
	hs := home(t, 80, 24)
	dev, aws := hs.m.hostByName("atlas-dev"), hs.m.hostByName("aws-gpu")
	if !dev.online() || dev.rtt.mid != 9*time.Millisecond || dev.info == nil {
		t.Fatalf("atlas-dev: status %v, round trip %+v, info %v", dev.status, dev.rtt, dev.info)
	}
	if aws.status != stOffline {
		t.Fatalf("aws-gpu status %v, want offline", aws.status)
	}
	if hs.m.screen != scrHosts || hs.m.selectedHost() != dev {
		t.Fatal("the launcher does not open on the hosts, with the first selected")
	}
	if hs.m.preview.key != (sessKey{}) {
		t.Fatal("a preview loaded with no one asking")
	}
	hs.key("enter")
	if hs.m.sel != (sessKey{"atlas-dev", "s4"}) {
		t.Fatalf("selected %v, want the session that needs input", hs.m.sel)
	}
}

func TestQuit(t *testing.T) {
	hs := home(t, 80, 24)
	hs.key("q")
	if !hs.quit {
		t.Fatal("q did not quit")
	}

	hs = home(t, 80, 24)
	hs.key("ctrl+c")
	if hs.quit || !hs.m.quitArmed {
		t.Fatal("one ctrl+c quit, or did not arm the chord")
	}
	if !strings.Contains(plainScreen(hs.m), "ctrl+c again to quit") {
		t.Fatal("the footer does not say what the chord does")
	}
	hs.key("esc")
	if hs.quit || hs.m.quitArmed {
		t.Fatal("esc did not cancel the chord")
	}
	hs.key("ctrl+c", "ctrl+c")
	if !hs.quit {
		t.Fatal("ctrl+c ctrl+c did not quit")
	}
}

func TestHostsThenSessions(t *testing.T) {
	hs := home(t, 80, 24)
	hs.key("down")
	if h := hs.m.selectedHost(); h.name() != "aws-gpu" {
		t.Fatalf("down selected %v", h.name())
	}
	hs.key("up", "enter")
	if hs.m.screen != scrSessions || hs.m.cur != "atlas-dev" {
		t.Fatalf("enter opened %v %q", hs.m.screen, hs.m.cur)
	}
	if strings.Contains(plainScreen(hs.m), "gcp-dev") {
		t.Fatal("another host shows on the sessions screen")
	}
	hs.key("down")
	if hs.m.sel.id != "s3" {
		t.Fatalf("down selected %v", hs.m.sel)
	}
	hs.key("esc")
	if hs.m.screen != scrHosts || hs.m.selectedHost().name() != "atlas-dev" {
		t.Fatal("esc did not go back to the hosts, on the same host")
	}
	hs.key("l")
	if hs.m.screen != scrSessions {
		t.Fatal("l did not open the host")
	}
	hs.key("h")
	if hs.m.screen != scrHosts {
		t.Fatal("h did not go back")
	}

	// With one host, its sessions are the first screen.
	be := labBackend()
	be.hosts = be.hosts[:1]
	if one := newHarness(t, be, 80, 24); one.m.screen != scrSessions || one.m.cur != "atlas-dev" {
		t.Fatal("one host did not open on its sessions")
	}
}

func TestNewSessionAttaches(t *testing.T) {
	hs := home(t, 80, 24)
	hs.key("n")
	p := hs.m.prompt
	if p == nil || p.mode != promptNew || p.host != "atlas-dev" {
		t.Fatalf("n opened %+v", p)
	}
	hs.typeText("cod")
	hs.key("tab") // completes the agent
	if got := p.in.value(); got != "codex" {
		t.Fatalf("tab completed to %q", got)
	}
	hs.typeText(" ~/src/api api-fix +wt")
	hs.key("enter")
	hasCall(t, hs.be, "new atlas-dev agent=codex cwd=~/src/api name=api-fix worktree=true size=80x24")
	if hs.m.prompt != nil {
		t.Fatal("the prompt stayed open")
	}
	if len(hs.be.attached) != 1 || hs.be.attached[0].Session != fakeSessionID(5) {
		t.Fatalf("attached %+v", hs.be.attached)
	}
	o := hs.be.attached[0]
	if o.State != hs.m.attach || !o.Interactive {
		t.Fatalf("attach options %+v", o)
	}
	if hs.m.screen != scrSessions || hs.m.cur != "atlas-dev" || hs.m.sel.id != fakeSessionID(5) {
		t.Fatal("after the detach the launcher is not on the session")
	}
	hasToast(t, hs.m, "Detached. api-fix keeps running on atlas-dev.")
	if hs.m.lastAgent["atlas-dev"] != "codex" {
		t.Fatal("the agent was not remembered")
	}
	hs.key("n")
	if !strings.HasPrefix(hs.m.prompt.in.placeholder, "codex ") {
		t.Fatalf("the next quick start suggests %q", hs.m.prompt.in.placeholder)
	}
}

func TestNewSessionWithTheForm(t *testing.T) {
	hs := home(t, 80, 24)
	hs.key("n")
	hs.typeText("codex ~/src/api")
	hs.key("ctrl+f")
	d, ok := hs.m.modal.(*newSession)
	if !ok || hs.m.prompt != nil {
		t.Fatalf("ctrl+f opened %T", hs.m.modal)
	}
	if d.agent() != "codex" || d.folder.value() != "~/src/api" {
		t.Fatalf("the form did not take the line: %q %q", d.agent(), d.folder.value())
	}
	hs.key("tab", "tab")
	hs.typeText("api-fix")
	hs.key("tab", "right", "enter")
	hasCall(t, hs.be, "new atlas-dev agent=codex cwd=~/src/api name=api-fix worktree=true size=80x24")
	if hs.m.modal != nil || len(hs.be.attached) != 1 {
		t.Fatal("the form did not start and attach")
	}
}

func TestQuickStartCompletesFolders(t *testing.T) {
	hs := home(t, 80, 24)
	hs.key("n")
	hs.typeText("claude ~/src/a")
	p := hs.m.prompt
	if g := hs.m.ghost(); g != "pi/" {
		t.Fatalf("ghost %q, folders %v", g, p.folders)
	}
	hs.key("tab")
	if got := p.in.value(); got != "claude ~/src/api/" {
		t.Fatalf("tab completed to %q", got)
	}
	if a := quickArgs("shell ~/x my name +wt", []string{"claude", "shell"}, "claude"); a.Agent != "shell" || a.Cwd != "~/x" || a.Name != "my-name" || !a.Worktree {
		t.Fatalf("quickArgs = %+v", a)
	}
}

func TestNewSessionDetached(t *testing.T) {
	hs := home(t, 80, 24)
	hs.key("n", "ctrl+enter")
	if len(hs.be.attached) != 0 {
		t.Fatal("ctrl+enter attached")
	}
	hasToast(t, hs.m, "Started app on atlas-dev.")
	if hs.m.sel.id != fakeSessionID(5) || hs.m.screen != scrSessions {
		t.Fatalf("the new session is not selected: %v", hs.m.sel)
	}
}

func TestNewSessionError(t *testing.T) {
	hs := home(t, 80, 24)
	hs.be.state["atlas-dev"].newErr = &client.UserError{Msg: "codex is not installed on this host", Hint: "Install it on the host, then try again."}
	hs.key("n", "enter")
	p := hs.m.prompt
	if p == nil || p.err == nil || p.starting {
		t.Fatalf("the error is not on the prompt: %+v", p)
	}
	out := plainScreen(hs.m)
	if !strings.Contains(out, "✕ Codex is not installed on this host.") || !strings.Contains(out, "→ Install it on the host, then try again.") {
		t.Fatalf("the error or its next step is missing:\n%s", out)
	}
}

func TestAttachKeepsStateAndReportsEnd(t *testing.T) {
	hs := sessions(t, 80, 24)
	hs.key("enter")
	hasToast(t, hs.m, "Detached. auth-refactor keeps running on atlas-dev.")
	hs.be.attach = client.AttachResult{Ended: &proto.Ended{ExitCode: 2}}
	hs.key("enter")
	if len(hs.be.attached) != 2 || hs.be.attached[0].State != hs.be.attached[1].State {
		t.Fatal("the two attaches did not share one AttachState")
	}
	if st := hs.m.attach; !st.Valid || st.Key != "atlas-dev/s4" || st.Offset != 200 {
		t.Fatalf("attach state %+v", st)
	}
	hasToast(t, hs.m, "auth-refactor on atlas-dev ended (exit 2).")

	hs.be.attach = client.AttachResult{Reconnects: 1, Missed: 3 << 20}
	hs.key("enter")
	hasToast(t, hs.m, "Reconnected to atlas-dev (missed 3 MiB, replayed).")

	hs.be.attach, hs.be.attachErr = client.AttachResult{}, &client.UserError{Msg: "cannot reach atlas-dev: timed out", Hint: "ssh atlas-dev"}
	hs.key("enter")
	hasToast(t, hs.m, "Cannot reach atlas-dev: timed out.")
}

// An end out of memory says what to try next (AGENTS.md rule 7), and the
// end and its step both show at every size.
func TestAttachOOMSaysWhatToTry(t *testing.T) {
	end := &proto.Ended{ExitCode: 137, Signal: "killed", OOM: true}
	for _, sz := range sizes {
		hs := sessions(t, sz.w, sz.h)
		hs.be.attach = client.AttachResult{Ended: end}
		hs.key("enter")
		out := plainScreen(hs.m)
		if !strings.Contains(out, "● auth-refactor on atlas-dev ended (out of memory).") || !strings.Contains(out, "→ "+endHint(end)) {
			t.Fatalf("at %dx%d the end or its next step is missing:\n%s", sz.w, sz.h, out)
		}
	}
}

func TestAttachEndedSession(t *testing.T) {
	hs := sessions(t, 80, 24)
	hs.key("end", "enter")
	if len(hs.be.attached) != 0 {
		t.Fatal("attached to an ended session")
	}
	// Its holder has stopped too, so there is no last screen to go to.
	hasToast(t, hs.m, "docs-pass on atlas-dev ended (exit 0), and its holder has stopped.")
}

func TestDeleteConfirm(t *testing.T) {
	hs := sessions(t, 80, 24)
	hs.key("down", "d")
	c, ok := hs.m.modal.(*confirm)
	if !ok || c.title != "Delete api-tests?" || !c.danger {
		t.Fatalf("d opened %T", hs.m.modal)
	}
	hs.key("esc")
	if hs.m.modal != nil || slices.ContainsFunc(hs.be.callList(), func(s string) bool { return strings.HasPrefix(s, "delete") }) {
		t.Fatal("esc did not keep the session")
	}
	hs.key("d", "enter")
	hasCall(t, hs.be, "delete atlas-dev s3")
	hasToast(t, hs.m, "Deleted api-tests on atlas-dev. Kept the worktree ~/src/api-tests: it has uncommitted changes.")
	for _, r := range hs.m.rows() {
		if r.s.ID == "s3" {
			t.Fatal("the deleted session is still listed")
		}
	}
	if hs.m.sel.id == "s3" || hs.m.sel == (sessKey{}) {
		t.Fatalf("selection after delete: %v", hs.m.sel)
	}
}

// A delete whose worktree an error kept is a warning, with the host's name
// in it and the step to try under it, not a success.
func TestDeleteKeptWorktreeWarns(t *testing.T) {
	hs := sessions(t, 80, 24)
	hs.be.deleted = &client.DeleteResult{Deleted: true,
		Worktree:     "kept the worktree ~/w (branch b): could not check it: could not start git in a scope on this host: Failed to connect to bus",
		WorktreeCode: api.CodeGitScope, WorktreeHint: "Run the host doctor."}
	hs.key("down", "d", "enter")
	want := "Deleted api-tests on atlas-dev. Kept the worktree ~/w (branch b): could not check it: could not start git in a scope on atlas-dev: Failed to connect to bus."
	var found *toast
	for _, x := range append(append([]toast(nil), hs.m.toasts.shown...), hs.m.toasts.queue...) {
		if x.text == want {
			found = &x
		}
	}
	if found == nil || found.kind != toastWarning || found.hint != brand.Name+" host doctor atlas-dev" {
		t.Fatalf("toast = %+v; have %q", found, toastTexts(hs.m))
	}
}

// The launcher's delete is bound to the name that its question showed
// (review 7, finding 6): a session renamed while the question waits, and
// listed since under its new name, stays.
func TestADeleteIsBoundToTheNameShown(t *testing.T) {
	hs := sessions(t, 80, 24)
	hs.key("down", "d")
	if c, ok := hs.m.modal.(*confirm); !ok || c.title != "Delete api-tests?" {
		t.Fatalf("d opened %T", hs.m.modal)
	}
	hs.be.mu.Lock()
	for _, h := range hs.be.state {
		for i := range h.sessions {
			if h.sessions[i].ID == "s3" {
				h.sessions[i].Name = "renamed"
			}
		}
	}
	hs.be.mu.Unlock()
	for _, h := range hs.m.hosts {
		for i := range h.sessions {
			if h.sessions[i].ID == "s3" {
				h.sessions[i].Name = "renamed"
			}
		}
	}
	hs.key("enter")
	hasCall(t, hs.be, "delete atlas-dev s3")
	if !slices.ContainsFunc(hs.m.rows(), func(r sessRow) bool { return r.s.ID == "s3" }) {
		t.Fatal("the renamed session was deleted")
	}
	hs.key("d", "enter")
	if slices.ContainsFunc(hs.m.rows(), func(r sessRow) bool { return r.s.ID == "s3" }) {
		t.Fatal("a delete confirmed under the new name did not delete it")
	}
}

func TestRename(t *testing.T) {
	hs := sessions(t, 80, 24)
	hs.key("r")
	if hs.m.rename == nil {
		t.Fatal("r did not start a rename")
	}
	hs.key("ctrl+u")
	hs.typeText("auth-v2")
	hs.key("enter")
	hasCall(t, hs.be, "rename atlas-dev s4 auth-v2")
	hasToast(t, hs.m, "Renamed auth-refactor to auth-v2 on atlas-dev.")
	if hs.m.rename != nil {
		t.Fatal("the rename stayed open")
	}
	hs.key("r", "esc")
	if hs.m.rename != nil {
		t.Fatal("esc did not cancel")
	}
}

// A name of the form of an ID is refused before any request, as the CLI
// and the helper refuse it (cli-spec 9.2): the rename stays open, and the
// quick start says why in its line.
func TestNamesOfAnIDsFormAreRefused(t *testing.T) {
	hs := sessions(t, 80, 24)
	hs.key("r", "ctrl+u")
	hs.typeText("abcd2345")
	hs.key("enter")
	for _, c := range hs.be.callList() {
		if strings.HasPrefix(c, "rename ") {
			t.Fatalf("the launcher sent %q", c)
		}
	}
	if hs.m.rename == nil {
		t.Fatal("the rename closed")
	}
	hasToast(t, hs.m, "A name of 8 letters a to z and digits 2 to 7, with a digit, looks like a session ID.")

	hn := home(t, 80, 24)
	hn.key("n")
	hn.typeText("codex ~/src/api abcd2345")
	hn.key("enter")
	for _, c := range hn.be.callList() {
		if strings.HasPrefix(c, "new ") {
			t.Fatalf("the launcher sent %q", c)
		}
	}
	var ue *client.UserError
	if p := hn.m.prompt; p == nil || !errors.As(p.err, &ue) || ue.Code != "bad_name" {
		t.Fatalf("the quick start: %+v", hn.m.prompt)
	}
}

func TestCopyAttachCommand(t *testing.T) {
	hs := sessions(t, 80, 24)
	_, cmd := hs.m.Update(keyMsg("y"))
	if cmd == nil {
		t.Fatal("y returned no command")
	}
	hs.do(cmd)
	// By host/ID: a name can mean another session (cli-spec 9.2).
	hasToast(t, hs.m, "Copied: "+brand.Name+" attach --id -- atlas-dev/s4.")
}

func TestSearchEveryHost(t *testing.T) {
	hs := home(t, 80, 24)
	hs.key("/")
	if hs.m.prompt == nil || hs.m.prompt.mode != promptSearch {
		t.Fatal("/ did not open the search")
	}
	if n := len(hs.m.results()); n != 6 {
		t.Fatalf("an empty search lists %d sessions, want all 6", n)
	}
	hs.typeText("gcp")
	if n := len(hs.m.results()); n != 2 {
		t.Fatalf("a host's name finds %d sessions, want its 2", n)
	}
	hs.key("ctrl+u")
	hs.typeText("bench")
	rs := hs.m.results()
	if len(rs) != 1 || rs[0].s.ID != "g6" {
		t.Fatalf("results %v", rs)
	}
	hs.key("enter")
	if len(hs.be.attached) != 1 || hs.be.attached[0].Session != "g6" || hs.m.cur != "gcp-dev" {
		t.Fatal("enter did not attach the match")
	}
	hs.key("/", "esc")
	if hs.m.prompt != nil {
		t.Fatal("esc did not close the search")
	}
	hs.key("alt+e")
	if n := sessionRows(hs.m.rows()); n != 1 {
		t.Fatalf("alt+e left %d rows on gcp-dev, want 1", n)
	}
}

func TestPasteSessionID(t *testing.T) {
	hs := home(t, 80, 24)
	hs.send(tea.PasteMsg{Content: "gcp-dev/g2\n"})
	if hs.m.sel != (sessKey{"gcp-dev", "g2"}) || hs.m.screen != scrSessions || hs.m.cur != "gcp-dev" {
		t.Fatalf("paste selected %v", hs.m.sel)
	}
}
