package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

func TestDowngradedNoneKeepsEveryExplicitExposureWarning(t *testing.T) {
	s := client.Session{Wall: &api.WallInfo{Level: "none", DowngradedFrom: "process"}, Secrets: []string{"Z_TOKEN", "A_TOKEN"}}
	setExposedDisplaySummary(&s, s.Armed)
	warnings := strings.Join(sessionWallWarnings(s), "\n")
	for _, text := range []string{"below the floor process", "exposed: A_TOKEN", "exposed: Z_TOKEN", "secrets not armed"} {
		if !strings.Contains(warnings, text) {
			t.Fatalf("downgraded-none warning lost %q: %s", text, warnings)
		}
	}
}

func TestSelectedWallRemainsVisibleOnNarrowForms(t *testing.T) {
	for _, level := range wallChoices {
		m := &Model{}
		d := &newSession{wall: level}
		for _, width := range []int{18, 32, 48} {
			row := ansi.Strip(d.wallRow(m, width))
			if !strings.Contains(row, "● "+level) || cells(row) > width {
				t.Fatalf("selected %s is hidden at width %d: %q", level, width, row)
			}
		}
	}
}

func TestClosedDowngradeSheetCannotApproveOrAcceptPaste(t *testing.T) {
	hs := home(t, 80, 24)
	form := &newSession{starting: true}
	d := &downgradeSheet{request: startReq{host: "atlas-dev", form: form, args: client.NewArgs{Wall: "process"}},
		level: "process", floor: "microvm", typed: newInput("process", "")}
	hs.m.modal = d
	_ = hs.m.disposeModal()
	before := d.typed.value()
	d.paste("changed")
	if d.key(hs.m, keyMsg("enter")) != nil || form.downgrade || d.typed.value() != before {
		t.Fatal("a disposed downgrade sheet still changed typed intent")
	}
}
