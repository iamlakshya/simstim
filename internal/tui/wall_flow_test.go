package tui

import (
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/iamlakshya/simstim/internal/client"
)

func TestStartGateRefusesBeforeSecretFetch(t *testing.T) {
	hs := home(t, 80, 24)
	hs.be.mu.Lock()
	hs.be.state["atlas-dev"].grantErr = &client.UserError{Msg: "the helper cannot preserve the request tier",
		Hint: "Update the host helper.", Code: "helper_old"}
	hs.be.mu.Unlock()
	hs.key("n")
	hs.typeText("shell")
	hs.key("enter")
	hasCall(t, hs.be, "grants:atlas-dev")
	for _, call := range hs.be.callList() {
		if strings.HasPrefix(call, "secrets ") || strings.HasPrefix(call, "new ") {
			t.Fatalf("refused grant reached a value fetch or a start: %s", call)
		}
	}
}

func TestDowngradeSheetRequiresTheExactLevel(t *testing.T) {
	hs := home(t, 80, 24)
	form := &newSession{starting: true}
	request := startReq{host: "atlas-dev", form: form, args: client.NewArgs{Wall: "process"}}
	d := &downgradeSheet{request: request, level: "process", floor: "microvm", typed: newInput("", "process")}
	hs.m.modal = d
	for _, answer := range []string{"", "yes", "Process", " process"} {
		d.typed.set(answer)
		if cmd := d.key(hs.m, keyMsg("enter")); cmd != nil || form.downgrade || hs.m.modal != d {
			t.Fatalf("%q approved a downgrade", answer)
		}
	}
	d.typed.set("process")
	_ = d.key(hs.m, keyMsg("enter"))
	if !form.downgrade || hs.m.modal != form {
		t.Fatal("the exact level did not return to the original start")
	}
	for _, call := range hs.be.callList() {
		if strings.HasPrefix(call, "new ") {
			t.Fatal("building a retry executed it before the model owned it")
		}
	}
}

func TestDowngradePasteIsTextAndCancelKeepsTheFloor(t *testing.T) {
	form := &newSession{starting: true}
	m := &Model{}
	d := &downgradeSheet{request: startReq{form: form}, level: "process", floor: "microvm", typed: newInput("", "process")}
	m.modal = d
	d.paste("process")
	if form.downgrade {
		t.Fatal("pasting a level approved it")
	}
	_ = d.key(m, keyMsg("esc"))
	if form.downgrade || form.starting || m.modal != form {
		t.Fatal("cancel changed the requested wall or lost the original form")
	}
}

func TestDefaultWallAndExplicitNoneStayDistinct(t *testing.T) {
	d := &newSession{}
	if d.wall != "" {
		t.Fatal("the form changed its original unspecified wall intent")
	}
	row := d.wallRow(&Model{}, 80)
	if !strings.Contains(row, "● auto") {
		t.Fatal("unspecified wall was not drawn as the pinned-floor default", row)
	}
	d.wallKey("left")
	if d.wall != "none" || d.downgrade {
		t.Fatal("choosing none lost explicit intent or silently approved a downgrade")
	}
	d.wallKey("right")
	if d.wall != "auto" || d.downgrade {
		t.Fatal("returning to auto altered its floor selection or approval")
	}
}

func TestDowngradeSheetGoldenText(t *testing.T) {
	m := &Model{width: 100, height: 36}
	d := &downgradeSheet{request: startReq{host: "box"}, level: "process", floor: "microvm", typed: newInput("", "process")}
	view := strings.Join(strings.Fields(ansi.Strip(d.view(m))), " ")
	want, err := os.ReadFile("testdata/wall_downgrade.golden")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(want)), "\n") {
		if !strings.Contains(view, line) {
			t.Errorf("the real downgrade sheet omits %q", line)
		}
	}
}
