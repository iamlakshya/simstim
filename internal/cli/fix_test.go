package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

var sampleFixes = []api.Fix{
	{ID: "linger", Problem: "linger is off", Action: "turn on linger", Command: "loginctl enable-linger me"},
	{ID: "terminfo", Problem: "Ghostty's terminfo is missing", Action: "run it from Ghostty", Command: "infocmp -x xterm-ghostty | ssh box tic -x -", Manual: true},
	{ID: "sockets", Problem: "2 stale session sockets", Action: "remove them", Command: "rm /run/a.sock /run/b.sock"},
}

func TestOfferFixesAsksForEach(t *testing.T) {
	var out bytes.Buffer
	var asked []string
	err := offerFixes(testPrompts("y\nn\n", &out), qFix, sampleFixes, false, func(ids []string) ([]api.Fix, error) {
		asked = ids
		res := append([]api.Fix(nil), sampleFixes...)
		res[0].Done = true
		return res, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(asked, ",") != "linger" {
		t.Fatalf("applied %v, want only linger", asked)
	}
	s := out.String()
	for _, want := range []string{
		"Found 3 to fix:",
		"1. Linger is off.\n   Fix: turn on linger.\n   Runs: loginctl enable-linger me\n   Fix it? [y/N]",
		"2. Ghostty's terminfo is missing.\n   This one is yours to do: run it from Ghostty.\n   Run: infocmp -x xterm-ghostty | ssh box tic -x -",
		"✓ fixed: linger is off",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Count(s, "Fix it?") != 2 {
		t.Errorf("asked %d times, want 2 (not for the manual fix):\n%s", strings.Count(s, "Fix it?"), s)
	}
}

func TestOfferFixesYesAndFailures(t *testing.T) {
	var out bytes.Buffer
	err := offerFixes(testPrompts("", &out), qFix, sampleFixes, true, func(ids []string) ([]api.Fix, error) {
		if strings.Join(ids, ",") != "linger,sockets" {
			t.Fatalf("applied %v", ids)
		}
		res := append([]api.Fix(nil), sampleFixes...)
		res[0].Error = "loginctl refused. It needs root on this host: run sudo loginctl enable-linger me"
		res[2].Done = true
		return res, nil
	})
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Msg != "1 of 2 fixes failed" {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out.String(), "✕ not fixed: linger is off\n    loginctl refused. It needs root on this host: run sudo loginctl enable-linger me") {
		t.Fatalf("output:\n%s", out.String())
	}

	out.Reset()
	if err := offerFixes(testPrompts("\n\n", &out), qFix, sampleFixes, false, func([]string) ([]api.Fix, error) {
		t.Fatal("applied with no yes")
		return nil, nil
	}); err != nil || !strings.Contains(out.String(), "Nothing was changed.") {
		t.Fatalf("no answers: %v\n%s", err, out.String())
	}
	out.Reset()
	if err := offerFixes(testPrompts("", &out), qFix, nil, false, nil); err != nil || !strings.Contains(out.String(), "Nothing to fix.") {
		t.Fatalf("nothing to fix: %v %q", err, out.String())
	}
}
