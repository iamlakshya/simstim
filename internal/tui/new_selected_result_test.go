package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
)

func selectedResultFixture(t *testing.T, hs *harness) (*newSession, *newSelectedStart, client.Host, startedMsg) {
	t.Helper()
	actual := hs.m.hostByName("gcp-dev").host
	d := newNeutralSessionDialog(hostSelectionPreview{automatic: true, reason: "automatic placement requested"})
	d.starting = true
	hs.m.modal = d
	ctx, cancel := context.WithCancel(hs.m.backgroundContext())
	t.Cleanup(cancel)
	owner := &newSelectedStart{ctx: ctx, cancel: cancel, original: startReq{form: d}, screen: hs.m.screen, current: hs.m.cur}
	hs.m.selectedNew = owner
	result := startedMsg{startReq: startReq{host: actual.Name, form: d, attach: true, selected: owner,
		args: client.NewArgs{Agent: "shell", Name: "actual-work"}},
		s: &client.Session{ID: "real2345", Name: "actual-work"}}
	return d, owner, actual, result
}

func TestSelectedStartResultAttachesToTheActualHost(t *testing.T) {
	hs := home(t, 100, 36)
	hs.m.hostSel = 0 // atlas is highlighted; the actual attempt chose gcp
	_, owner, actual, result := selectedResultFixture(t, hs)
	hs.do(hs.m.onSelectedFinished(newSelectedFinishedMsg{owner: owner, host: actual, result: result}))
	if len(hs.be.attached) != 1 || !client.SameEntry(hs.be.attached[0].Host, actual) || hs.be.attached[0].Session != "real2345" {
		t.Fatal("the actual start result acquired a highlighted or reused attach target", hs.be.attached)
	}
	if len(hs.be.newArgs) != 0 || len(hs.be.secretsFrom) != 0 {
		t.Fatal("publishing an actual result repeated a start or value read")
	}
}

func TestSelectedStartResultCannotAttachThroughAReplacedSavedEntry(t *testing.T) {
	hs := home(t, 100, 36)
	d, owner, actual, result := selectedResultFixture(t, hs)
	current := hs.m.hostByName(actual.Name)
	current.host.SSH = "replacement.example"
	before := len(current.sessions)
	hs.do(hs.m.onSelectedFinished(newSelectedFinishedMsg{owner: owner, host: actual, result: result}))
	if len(hs.be.attached) != 0 || len(current.sessions) != before || d.starting || d.err == nil {
		t.Fatal("a changed saved target gained another attempt's result or attach")
	}
	if !strings.Contains(strings.Join(toastTexts(hs.m), "\n"), "Started actual-work on gcp-dev") {
		t.Fatal("a successful remote start was hidden after its saved entry changed")
	}
	for _, call := range hs.be.callList() {
		if strings.HasPrefix(call, "delete ") || strings.HasPrefix(call, "new ") {
			t.Fatal("result publication deleted or recreated the remote session", call)
		}
	}
}

func TestRetiredSelectedFormCannotAskOrAttachFromALateResult(t *testing.T) {
	hs := home(t, 100, 36)
	_, owner, actual, result := selectedResultFixture(t, hs)
	hs.m.modal = nil
	hs.m.selectedNew = nil
	owner.cancel()
	hs.do(hs.m.onSelectedFinished(newSelectedFinishedMsg{owner: owner, host: actual, result: result}))
	if hs.m.modal != nil || len(hs.be.attached) != 0 || len(hs.be.newArgs) != 0 {
		t.Fatal("a retired real start result acquired a question, start or terminal")
	}
}

func TestSelectedStartOwnerEndsWhenItsScreenOrFormLeaves(t *testing.T) {
	for _, change := range []string{"screen", "current", "form"} {
		hs := home(t, 100, 36)
		_, owner, _, _ := selectedResultFixture(t, hs)
		switch change {
		case "screen":
			hs.m.screen = scrSessions
		case "current":
			hs.m.cur = "another-host"
		case "form":
			hs.m.modal = newNeutralSessionDialog(hostSelectionPreview{automatic: true})
		}
		hs.do(hs.m.retireSelectedNew())
		if owner.ctx.Err() == nil || hs.m.selectedNew != nil || len(hs.be.newArgs) != 0 || len(hs.be.attached) != 0 {
			t.Fatal("a retired view retained real start ownership", change)
		}
	}
}
