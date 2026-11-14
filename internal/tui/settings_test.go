package tui

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// keptSettings are settings that a session start can tell apart from the
// defaults: inline mode, a cap of 4G, and the command of a secret.
func keptSettingsFor(be *fakeBackend) client.Settings {
	s := be.Settings()
	s.Agents.Inline = true
	s.Sessions.MemoryMax = "4G"
	s.Secrets = map[string]string{"TOKEN": "op read op://dev/token"}
	return s
}

const brokenWarning = "config.toml has an error on line 2, so its settings are not used"

// setFile makes config.toml hold s, with the warning w: what the next read
// of the file gives.
func setFile(be *fakeBackend, s client.Settings, w string) {
	be.mu.Lock()
	defer be.mu.Unlock()
	be.settings, be.settingsWarn = &s, w
}

// usedKept checks that the last start used the kept settings, and the last
// fetch of secrets their commands.
func usedKept(t *testing.T, be *fakeBackend, starts bool) {
	t.Helper()
	be.mu.Lock()
	defer be.mu.Unlock()
	if starts {
		if len(be.newArgs) == 0 {
			t.Fatal("nothing started")
		}
		if a := be.newArgs[len(be.newArgs)-1]; !a.Inline || a.MemoryMax != 4<<30 {
			t.Errorf("the session started with inline %v and a cap of %d, want the kept true and 4G", a.Inline, a.MemoryMax)
		}
		return
	}
	if len(be.secretsFrom) == 0 {
		t.Fatal("no secrets were fetched")
	}
	if s := be.secretsFrom[len(be.secretsFrom)-1]; s.Secrets["TOKEN"] != "op read op://dev/token" {
		t.Errorf("the secrets came from %+v, not the kept settings", s.Secrets)
	}
}

// Each action that uses the settings, after an edit that broke config.toml,
// gets the settings that the launcher kept: a start (its inline mode, its
// cap and its secrets), a resume (its secrets), and the start of an
// agent's login in the add-host wizard (its inline mode and its cap).
func TestActionsAfterABrokenEdit(t *testing.T) {
	// A read of the real config.toml here gives the defaults.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	breakAfter := func(hs *harness) {
		setFile(hs.be, keptSettingsFor(hs.be), "")
		hs.send(editorDoneMsg{})
		setFile(hs.be, client.DefaultSettings(), brokenWarning)
		hs.send(editorDoneMsg{})
	}
	t.Run("start", func(t *testing.T) {
		hs := home(t, 80, 24)
		breakAfter(hs)
		hs.key("n", "enter")
		usedKept(t, hs.be, true)
		usedKept(t, hs.be, false)
	})
	t.Run("resume", func(t *testing.T) {
		hs := endedLab(t, 80, 24)
		h := hs.m.hostByName("gcp-dev")
		h.host.Secrets = []string{"TOKEN"}
		for i := range h.sessions {
			if h.sessions[i].ID == "g3" {
				h.sessions[i].Secrets = []string{"TOKEN"}
			}
		}
		hs.be.secrets = map[string]api.Secret{"TOKEN": "t0k"}
		breakAfter(hs)
		hs.key("u")
		if calls := resumeCalls(hs.be); len(calls) != 1 {
			t.Fatalf("resume calls %q", calls)
		}
		usedKept(t, hs.be, false)
	})
	t.Run("wizard", func(t *testing.T) {
		be := newFakeBackend()
		be.probe = client.Platform{OS: "linux", Arch: "arm64"}
		be.installed = true
		be.state["pi"] = &fakeHost{checks: []client.AgentCheck{agentReady("claude", "Claude Code", api.LoginNo)}}
		hs := newHarness(t, be, 80, 24)
		breakAfter(hs)
		hs.key("a")
		hs.typeText("pi")
		hs.key("enter", "enter", "enter", "enter")
		w := hs.m.modal.(*wizard)
		if w.step != wzRun || w.act.Kind != "login" {
			t.Fatalf("step %v, act %+v, err %v", w.step, w.act, w.err)
		}
		hs.key("enter")
		usedKept(t, be, true)
	})
}

// The real backend fetches secrets with the commands of the settings that
// it gets, not with those of config.toml, which here has none.
func TestRealBackendSecretsUseTheSettings(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := client.DefaultSettings()
	s.Secrets = map[string]string{"TOKEN": "printf t0k"}
	ctx := context.Background()
	got, err := realBackend{}.Secrets(ctx, s, client.Host{Name: "box", Secrets: []string{"TOKEN"}})
	if err != nil || len(got) != 1 {
		t.Fatalf("Secrets = %v, %v", len(got), err)
	}
	if got, err = (realBackend{}).SessionSecrets(ctx, s, []string{"TOKEN"}); err != nil || len(got) != 1 {
		t.Fatalf("SessionSecrets = %v, %v", len(got), err)
	}
}

// A good edit of config.toml made outside the launcher counts at the next
// action, and the launcher keeps its settings. A broken one keeps the
// settings that the launcher has, and says so once.
func TestSettingsEditedOutsideTheLauncher(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	hs := home(t, 80, 24)
	setFile(hs.be, keptSettingsFor(hs.be), "")
	hs.key("n", "enter")
	usedKept(t, hs.be, true)
	usedKept(t, hs.be, false)
	if !hs.m.settings.Agents.Inline || hs.m.settings.Secrets["TOKEN"] == "" {
		t.Errorf("the launcher did not keep the new settings: %+v", hs.m.settings)
	}
	setFile(hs.be, client.DefaultSettings(), brokenWarning)
	want := brokenWarning + ". The launcher keeps the settings it had."
	for range 2 {
		hs.key("n", "enter")
		usedKept(t, hs.be, true)
		usedKept(t, hs.be, false)
	}
	if !slices.Contains(toastTexts(hs.m), want) {
		t.Fatalf("no warning: %q", toastTexts(hs.m))
	}
	// Once the warning is gone, the same broken file does not bring it
	// back. After a good read, a broken one warns again.
	expire := func() {
		hs.clock = hs.clock.Add(time.Hour)
		hs.send(toastTickMsg{})
	}
	expire()
	hs.key("n", "enter")
	if slices.Contains(toastTexts(hs.m), want) {
		t.Error("the same warning showed again")
	}
	setFile(hs.be, keptSettingsFor(hs.be), "")
	hs.key("n", "enter")
	setFile(hs.be, client.DefaultSettings(), brokenWarning)
	hs.key("n", "enter")
	if !slices.Contains(toastTexts(hs.m), want) {
		t.Error("after a good read, a broken file does not warn")
	}
}
