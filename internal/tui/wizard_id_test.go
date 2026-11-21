package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/onboard"
)

func TestWizardRejectsRawSessionIDsBeforeCaching(t *testing.T) {
	for _, id := range []string{"valid1\u200b", "\u202evalid1", "id; command", "id\nnext"} {
		t.Run(id, func(t *testing.T) {
			hs := home(t, 80, 24)
			h := hs.m.hostByName("atlas-dev")
			before := len(h.sessions)
			w := &wizard{host: client.Host{Name: "atlas-dev"}, seq: 1, step: wzRun, agentPart: agentPart{starting: true}}
			cmd := w.onActStarted(hs.m, actStartedMsg{owner: w, seq: 1, s: &client.Session{ID: id, Name: "install"}})
			var ue *client.UserError
			if cmd != nil || !errors.As(w.err, &ue) || ue.Code != "bad_session_id" {
				t.Fatalf("bad ID reached attach or no clear error: %v, %v", cmd, w.err)
			}
			if len(h.sessions) != before || w.waiting != "" || w.act.Session != "" {
				t.Fatalf("bad raw ID was stored: sessions %d to %d, waiting %q, action %q", before, len(h.sessions), w.waiting, w.act.Session)
			}
		})
	}
}

func TestWizardStopsWhenCompletionHasNoValidSession(t *testing.T) {
	for _, id := range []string{"", "good1\u200b"} {
		hs := home(t, 80, 24)
		f := onboard.NewFlow("atlas-dev", nil, nil)
		_ = f.Choose("claude")
		a := f.Next()
		a.Session = id
		w := &wizard{host: client.Host{Name: "atlas-dev"}, seq: 1, step: wzRun, saved: true, agentPart: agentPart{flow: f, act: a, checking: true}}
		cmd := w.onAgentChecks(hs.m, agentChecksMsg{owner: w, seq: 1, act: a})
		var ue *client.UserError
		if cmd != nil || !errors.As(w.err, &ue) || ue.Code != "bad_session_id" || w.last != nil || w.act.Session != id || f.Next().Agent.Kind != a.Agent.Kind {
			t.Fatalf("completion continued: %v, %v", cmd, w.err)
		}
	}
}

func TestWizardUsesConfiguredDetachKey(t *testing.T) {
	hs := home(t, 100, 30)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := client.ConfigDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[attach]\ndetach_key = 'ctrl+]'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := onboard.NewFlow("atlas-dev", nil, nil)
	_ = f.Choose("claude")
	w := &wizard{host: client.Host{Name: "atlas-dev"}, agentPart: agentPart{flow: f, act: f.Next()}}
	_, body, _ := w.runView(hs.m, 90)
	got := strings.Join(body, " ")
	if !strings.Contains(got, "ctrl+] leaves the session running") || strings.Contains(got, "ctrl+\\ leaves") {
		t.Fatalf("wrong detach key: %q", got)
	}
}
