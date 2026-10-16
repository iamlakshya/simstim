package tui

import (
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

// formIn opens the new-session form on atlas-dev, with its folder set.
func formIn(hs *harness, folder string) *newSession {
	hs.t.Helper()
	hs.key("n", "ctrl+f")
	d, ok := hs.m.modal.(*newSession)
	if !ok {
		hs.t.Fatalf("no form: %T", hs.m.modal)
	}
	d.folder.set(folder)
	return d
}

func noMkdir(t *testing.T, be *fakeBackend) {
	t.Helper()
	for _, c := range be.callList() {
		if strings.Contains(c, "mkdir") {
			t.Fatalf("the folder was made: %s", c)
		}
	}
}

// Esc while a start runs cancels it. The host's question that comes after
// opens nothing, and does not take the place of what the user opened since.
func TestCancelledStartAsksNothing(t *testing.T) {
	hs := folderLab(t)
	formIn(hs, "~/src/new-app")
	_, start := hs.m.Update(keyMsg("enter"))
	hs.key("esc")
	other := formIn(hs, "~/src/app")
	hs.do(start)
	if hs.m.modal != other || other.starting {
		t.Fatalf("after the question: modal %T", hs.m.modal)
	}
	hs.key("esc", "n")
	hs.typeText("claude ~/src/new-app")
	_, start = hs.m.Update(keyMsg("enter"))
	hs.key("esc")
	hs.do(start)
	if hs.m.modal != nil || hs.m.prompt != nil {
		t.Fatalf("after the question: modal %T, prompt %+v", hs.m.modal, hs.m.prompt)
	}
	noMkdir(t, hs.be)
}

// A cancelled start that the host takes still starts its session. The
// launcher says so, but does not attach to it.
func TestCancelledStartDoesNotAttach(t *testing.T) {
	hs := folderLab(t)
	hs.key("n")
	hs.typeText("claude ~/src/app")
	_, start := hs.m.Update(keyMsg("enter"))
	hs.key("esc")
	hs.do(start)
	if len(hs.be.attached) != 0 || hs.m.prompt != nil {
		t.Fatalf("attached %d, prompt %+v", len(hs.be.attached), hs.m.prompt)
	}
	hasToast(t, hs.m, "Started app on atlas-dev.")
}

// When the host goes while its question is open, the answer ends the start:
// the form or the quick-start line is ready to start again.
func TestAnswerAfterTheHostIsGone(t *testing.T) {
	hs := folderLab(t)
	d := formIn(hs, "~/src/new-app")
	hs.key("enter")
	if _, ok := hs.m.modal.(*makeFolderAsk); !ok {
		t.Fatalf("no question: %T", hs.m.modal)
	}
	hs.do(hs.m.removeHost("atlas-dev"))
	hs.key("y")
	if hs.m.modal != d || d.starting {
		t.Fatalf("after yes: modal %T, starting %v", hs.m.modal, d.starting)
	}
	hasToast(t, hs.m, "The host atlas-dev is no longer in the list.")

	hs = folderLab(t)
	hs.key("n")
	hs.typeText("claude ~/src/new-app")
	hs.key("enter")
	hs.do(hs.m.removeHost("atlas-dev"))
	hs.key("y")
	if p := hs.m.prompt; hs.m.modal != nil || p == nil || p.starting {
		t.Fatalf("after yes: modal %T, prompt %+v", hs.m.modal, hs.m.prompt)
	}
	noMkdir(t, hs.be)
}

// A start fetches the host's secrets once. The start after an answer sends
// the same values again.
func TestAStartFetchesItsSecretsOnce(t *testing.T) {
	hs := folderLab(t)
	hs.be.secrets = map[string]api.Secret{"ANTHROPIC_API_KEY": "sk-test-value"}
	hs.key("n")
	hs.typeText("claude ~/src/new-app")
	hs.key("enter", "y")
	var fetched, sent int
	for _, c := range hs.be.callList() {
		switch c {
		case "secrets atlas-dev":
			fetched++
		case "new atlas-dev secret ANTHROPIC_API_KEY=[redacted]":
			sent++
		}
	}
	if fetched != 1 || sent != 2 || len(hs.be.attached) != 1 {
		t.Fatalf("fetched %d, sent %d, attached %d: %q", fetched, sent, len(hs.be.attached), hs.be.callList())
	}
}
