package tui

import (
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// trustLab is the lab with a repository on atlas-dev whose setup script has
// no answer yet. Its scripts have the setup hash h1.
func trustLab(t *testing.T) *harness {
	t.Helper()
	be := labBackend()
	be.state["atlas-dev"].setupRepo = "/home/dev/src/app"
	be.state["atlas-dev"].setupHash = "h1"
	hs := newHarness(t, be, 80, 24)
	hs.m.motion = false
	return hs
}

func TestQuickStartAsksAboutASetupScript(t *testing.T) {
	hs := trustLab(t)
	hs.key("n")
	hs.typeText("claude ~/src/app")
	hs.key("enter")
	ask, ok := hs.m.modal.(*setupAsk)
	if !ok {
		t.Fatalf("no question; modal %T, calls %q", hs.m.modal, hs.be.callList())
	}
	if ask.q.Repo != "/home/dev/src/app" || ask.q.Hash != "h1" || len(hs.be.attached) != 0 {
		t.Fatalf("question %+v, attached %d", ask.q, len(hs.be.attached))
	}
	out := plainScreen(hs.m)
	for _, want := range []string{"Run its setup script?", "The repository ~/src/app on atlas-dev has a setup script", "on atlas-dev until the script", "y run it", "n skip it"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
	hs.key("y")
	// The yes keeps the question's hash, and the start goes again with it,
	// not with --setup.
	hasCall(t, hs.be, "remember atlas-dev /home/dev/src/app run=true hash=h1")
	hasCall(t, hs.be, "new atlas-dev setup_hashes=map[/home/dev/src/app:h1]")
	if len(hs.be.attached) != 1 || hs.m.prompt != nil || hs.m.modal != nil {
		t.Fatalf("after yes: attached %d, prompt %v, modal %T", len(hs.be.attached), hs.m.prompt, hs.m.modal)
	}

	// The next start in the repository asks nothing: the answer goes along.
	hs.key("n")
	hs.typeText("claude ~/src/app")
	hs.key("enter")
	if _, ok := hs.m.modal.(*setupAsk); ok || len(hs.be.attached) != 2 {
		t.Fatalf("asked again: modal %T, attached %d", hs.m.modal, len(hs.be.attached))
	}
}

// After a change to the script, the launcher asks again, and says that the
// script changed after the user trusted it. If the script changes again
// before the start after a yes, it asks again.
func TestSetupScriptChangedAsksAgain(t *testing.T) {
	hs := trustLab(t)
	if _, err := hs.be.RememberSetup("atlas-dev", "/home/dev/src/app", true, "h0"); err != nil {
		t.Fatal(err)
	}
	hs.key("n")
	hs.typeText("claude ~/src/app")
	hs.key("enter")
	ask, ok := hs.m.modal.(*setupAsk)
	if !ok || ask.past() != client.SetupChanged || ask.q.Hash != "h1" {
		t.Fatalf("no question about the change: modal %T, calls %q", hs.m.modal, hs.be.callList())
	}
	out := plainScreen(hs.m)
	if !strings.Contains(out, "The setup script of ~/src/app on atlas-dev changed after you trusted it") {
		t.Fatalf("the question does not say that the script changed:\n%s", out)
	}
	hs.be.state["atlas-dev"].setupHash = "h2"
	hs.key("y")
	hasCall(t, hs.be, "remember atlas-dev /home/dev/src/app run=true hash=h1")
	hasCall(t, hs.be, "new atlas-dev setup_hashes=map[/home/dev/src/app:h1]")
	ask, ok = hs.m.modal.(*setupAsk)
	if !ok || ask.past() != client.SetupChanged || ask.q.Hash != "h2" || len(hs.be.attached) != 0 {
		t.Fatalf("did not ask again on a change before the retry: modal %T, attached %d", hs.m.modal, len(hs.be.attached))
	}
	hs.key("y")
	hasCall(t, hs.be, "remember atlas-dev /home/dev/src/app run=true hash=h2")
	hasCall(t, hs.be, "new atlas-dev setup_hashes=map[/home/dev/src/app:h2]")
	if len(hs.be.attached) != 1 {
		t.Fatalf("after yes: attached %d", len(hs.be.attached))
	}
}

// A yes with no hash, as from version 1 of the file, claims no change: the
// question says that the yes came before this version checked the content.
func TestSetupScriptTrustedBeforeHashes(t *testing.T) {
	hs := trustLab(t)
	if _, err := hs.be.RememberSetup("atlas-dev", "/home/dev/src/app", true, ""); err != nil {
		t.Fatal(err)
	}
	hs.key("n")
	hs.typeText("claude ~/src/app")
	hs.key("enter")
	ask, ok := hs.m.modal.(*setupAsk)
	if !ok || ask.past() != client.SetupUnchecked {
		t.Fatalf("no question: modal %T, calls %q", hs.m.modal, hs.be.callList())
	}
	out := plainScreen(hs.m)
	if strings.Contains(out, "changed after") || !strings.Contains(out, "You trusted the setup script of ~/src/app on atlas-dev before") ||
		!strings.Contains(out, "version checked its content") {
		t.Fatalf("the question's words:\n%s", out)
	}
}

// When the answers file broke while the question was open, keeping the
// answer moves it aside, and a warning names the old file. The start goes
// on.
func TestSetupAnswerWarnsWhenTheFileBroke(t *testing.T) {
	hs := trustLab(t)
	hs.be.rememberWarn = &client.UserError{Msg: "could not read setup-trust.json, so each setup script asks again. The old file is now setup-trust.json.bad.",
		Hint: "Compare the two files, then delete /state/setup-trust.json.bad."}
	hs.key("n")
	hs.typeText("claude ~/src/app")
	hs.key("enter", "y")
	hasCall(t, hs.be, "remember atlas-dev /home/dev/src/app run=true hash=h1")
	if len(hs.be.attached) != 1 {
		t.Fatalf("the session did not start: calls %q", hs.be.callList())
	}
	var warned bool
	for _, n := range hs.m.toasts.shown {
		warned = warned || n.kind == toastWarning && strings.Contains(n.text, "The old file is now setup-trust.json.bad.") &&
			n.hint == "Compare the two files, then delete /state/setup-trust.json.bad."
	}
	if !warned {
		t.Fatalf("no warning in %+v", hs.m.toasts.shown)
	}
}

// A script that cannot run gives the launcher no question, and no
// --no-setup flag: it offers to start without the repository's scripts.
func TestSetupOutsideOffersToStartWithoutTheScript(t *testing.T) {
	for _, code := range []string{api.CodeSetupOutside, api.CodeSetupUnreadable} {
		hs := trustLab(t)
		hs.be.state["atlas-dev"].setupRepo = ""
		hs.be.state["atlas-dev"].setupErr = code
		hs.key("n")
		hs.typeText("claude ~/src/app")
		hs.key("enter")
		if _, ok := hs.m.modal.(*setupSkipAsk); !ok {
			t.Fatalf("%s: no offer; modal %T, calls %q", code, hs.m.modal, hs.be.callList())
		}
		out := plainScreen(hs.m)
		for _, want := range []string{"Start without its setup script?", "The setup script of /home/dev/src/app", "y start without it", "n cancel"} {
			if !strings.Contains(out, want) {
				t.Fatalf("%s: missing %q:\n%s", code, want, out)
			}
		}
		hs.key("y")
		hasCall(t, hs.be, "new atlas-dev setup=skip")
		if len(hs.be.attached) != 1 || hs.m.modal != nil {
			t.Fatalf("%s: after yes: attached %d, modal %T", code, len(hs.be.attached), hs.m.modal)
		}
	}
	// No cancels: the quick-start line comes back, and nothing starts.
	hs := trustLab(t)
	hs.be.state["atlas-dev"].setupRepo = ""
	hs.be.state["atlas-dev"].setupErr = api.CodeSetupOutside
	hs.key("n", "enter", "n")
	if hs.m.modal != nil || hs.m.prompt == nil || hs.m.prompt.starting || len(hs.be.attached) != 0 {
		t.Fatalf("after no: modal %T, prompt %+v, attached %d", hs.m.modal, hs.m.prompt, len(hs.be.attached))
	}
}

// An older helper sends no hash: the answer goes in Setup, for this start
// only, as before.
func TestSetupQuestionFromAnOlderHelper(t *testing.T) {
	hs := trustLab(t)
	hs.be.state["atlas-dev"].setupHash = ""
	hs.key("n")
	hs.typeText("claude ~/src/app")
	hs.key("enter")
	if _, ok := hs.m.modal.(*setupAsk); !ok {
		t.Fatalf("no question: %T", hs.m.modal)
	}
	hs.key("y")
	hasCall(t, hs.be, "remember atlas-dev /home/dev/src/app run=true hash=")
	hasCall(t, hs.be, "new atlas-dev setup=run")
}

// A file of answers that does not parse shows a warning, once, and the
// start goes on as if there were no answers.
func TestSetupAnswersThatDoNotParseWarn(t *testing.T) {
	hs := trustLab(t)
	hs.be.state["atlas-dev"].setupRepo = ""
	hs.be.trustWarn = &client.UserError{Msg: "could not read setup-trust.json, so each setup script asks again", Hint: "Compare the two files"}
	hs.key("n", "enter")
	if len(hs.be.attached) != 1 {
		t.Fatalf("the session did not start: calls %q", hs.be.callList())
	}
	// The notice after the attach can push the warning's first line above
	// the two rows of notices, so look in the queue.
	var warned int
	for _, n := range hs.m.toasts.shown {
		if n.kind == toastWarning && n.text == "Could not read setup-trust.json, so each setup script asks again." && n.hint == "Compare the two files" {
			warned++
		}
	}
	if warned != 1 {
		t.Fatalf("%d warnings in %+v", warned, hs.m.toasts.shown)
	}
	// It warns once: the next start reads no warning.
	hs.key("n", "enter")
	if len(hs.be.attached) != 2 {
		t.Fatalf("the second session did not start: calls %q", hs.be.callList())
	}
}

func TestFormSetupScriptSkipAndCancel(t *testing.T) {
	hs := trustLab(t)
	hs.key("n", "ctrl+f")
	if _, ok := hs.m.modal.(*newSession); !ok {
		t.Fatalf("no form: %T", hs.m.modal)
	}
	hs.key("enter")
	if _, ok := hs.m.modal.(*setupAsk); !ok {
		t.Fatalf("the form did not ask: %T", hs.m.modal)
	}
	// Esc cancels: the form comes back, ready to start again.
	hs.key("esc")
	d, ok := hs.m.modal.(*newSession)
	if !ok || d.starting {
		t.Fatalf("after esc: %T", hs.m.modal)
	}
	for _, c := range hs.be.callList() {
		if strings.HasPrefix(c, "remember") {
			t.Fatalf("esc kept an answer: %s", c)
		}
	}
	hs.key("enter")
	hs.key("n")
	hasCall(t, hs.be, "remember atlas-dev /home/dev/src/app run=false hash=h1")
	hasCall(t, hs.be, "new atlas-dev setup=skip")
	if hs.m.modal != nil || len(hs.be.attached) != 1 {
		t.Fatalf("after no: modal %T, attached %d", hs.m.modal, len(hs.be.attached))
	}
}

func TestQuickStartCancelSetupQuestion(t *testing.T) {
	hs := trustLab(t)
	hs.key("n", "enter", "esc")
	if hs.m.modal != nil || hs.m.prompt == nil || hs.m.prompt.starting {
		t.Fatalf("after esc: modal %T, prompt %+v", hs.m.modal, hs.m.prompt)
	}
	if len(hs.be.attached) != 0 {
		t.Fatal("a session started")
	}
}
