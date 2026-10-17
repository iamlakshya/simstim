package tui

import (
	"strings"
	"testing"
)

// folderLab is the lab with no ~/src/new-app on atlas-dev.
func folderLab(t *testing.T) *harness {
	t.Helper()
	be := labBackend()
	be.state["atlas-dev"].missing = "~/src/new-app"
	hs := newHarness(t, be, 80, 24)
	hs.m.motion = false
	return hs
}

func TestQuickStartAsksToMakeAFolder(t *testing.T) {
	hs := folderLab(t)
	hs.key("n")
	hs.typeText("claude ~/src/new-app")
	hs.key("enter")
	ask, ok := hs.m.modal.(*makeFolderAsk)
	if !ok {
		t.Fatalf("no question; modal %T, calls %q", hs.m.modal, hs.be.callList())
	}
	if ask.q.Folder != "/home/dev/src/new-app" || len(hs.be.attached) != 0 {
		t.Fatalf("question %+v, attached %d", ask.q, len(hs.be.attached))
	}
	out := plainScreen(hs.m)
	for _, want := range []string{"Make the folder?", "The folder ~/src/new-app does not exist on atlas-dev.", "y make it", "n cancel"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
	hs.key("y")
	hasCall(t, hs.be, "new atlas-dev mkdir ~/src/new-app")
	if len(hs.be.attached) != 1 || hs.m.prompt != nil || hs.m.modal != nil {
		t.Fatalf("after yes: attached %d, prompt %v, modal %T", len(hs.be.attached), hs.m.prompt, hs.m.modal)
	}
}

func TestFormFolderQuestionCancel(t *testing.T) {
	hs := folderLab(t)
	hs.key("n", "ctrl+f")
	d, ok := hs.m.modal.(*newSession)
	if !ok {
		t.Fatalf("no form: %T", hs.m.modal)
	}
	d.folder.set("~/src/new-app")
	hs.key("enter")
	if _, ok := hs.m.modal.(*makeFolderAsk); !ok {
		t.Fatalf("the form did not ask: %T", hs.m.modal)
	}
	// No cancels: the form comes back with its folder, ready to start again.
	hs.key("n")
	d, ok = hs.m.modal.(*newSession)
	if !ok || d.starting || d.folder.value() != "~/src/new-app" {
		t.Fatalf("after no: %T", hs.m.modal)
	}
	for _, c := range hs.be.callList() {
		if strings.Contains(c, "mkdir") {
			t.Fatalf("no made the folder: %s", c)
		}
	}
	hs.key("enter", "y")
	hasCall(t, hs.be, "new atlas-dev mkdir ~/src/new-app")
	if hs.m.modal != nil || len(hs.be.attached) != 1 {
		t.Fatalf("after yes: modal %T, attached %d", hs.m.modal, len(hs.be.attached))
	}
}

func TestQuickStartCancelFolderQuestion(t *testing.T) {
	hs := folderLab(t)
	hs.key("n")
	hs.typeText("claude ~/src/new-app")
	hs.key("enter", "esc")
	if hs.m.modal != nil || hs.m.prompt == nil || hs.m.prompt.starting {
		t.Fatalf("after esc: modal %T, prompt %+v", hs.m.modal, hs.m.prompt)
	}
	if len(hs.be.attached) != 0 {
		t.Fatal("a session started")
	}
}
