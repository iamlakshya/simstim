package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
)

func TestHostErrorsAreCleanedAtHumanDisplayBoundaries(t *testing.T) {
	raw := "host\x1b]52;c;value\x07\u009b\u202e\u200b\nnext"
	for _, err := range []error{errors.New(raw), &client.UserError{Msg: raw, Hint: raw, Code: "x"}} {
		msg, hint := errText(err)
		if strings.ContainsAny(msg+hint, "\x1b\x07\u009b\u202e\u200b") {
			t.Fatalf("unsafe errText: %q, %q", msg, hint)
		}
		hs := home(t, 100, 30)
		_ = hs.m.toastErr(err)
		if toasts := strings.Join(toastTexts(hs.m), " "); strings.ContainsAny(toasts, "\x1b\x07\u009b\u202e\u200b\n") {
			t.Fatalf("unsafe single-line toast boundary: %q", toasts)
		}
		got := strings.Join(toastTexts(hs.m), " ") + strings.Join(errorLines(err, 80), " ")
		w := &wizard{err: err, step: wzTest, host: client.Host{Name: "box"}}
		got += w.view(hs.m)
		g := &guide{step: gdWhere, err: err}
		got += g.view(hs.m)
		if strings.ContainsAny(got, "\x07\u009b\u202e\u200b") || strings.Contains(got, "]52;c;") && strings.Contains(got, "\x1b]52") {
			t.Fatalf("unsafe human boundary: %q", got)
		}
		var ue *client.UserError
		if errors.As(err, &ue) && (ue.Msg != raw || ue.Hint != raw) {
			t.Fatal("display mutated raw error")
		}
	}
}
