package tui

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
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
	s.Secrets = map[string]client.SecretDefinition{"TOKEN": {Run: "op read op://dev/token"}}
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
	if s := be.secretsFrom[len(be.secretsFrom)-1]; s.Secrets["TOKEN"].Run != "op read op://dev/token" {
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

// Kept settings are presentation state. Real secret commands always use the
// current file and its complete validation barrier, including unused entries.
func TestRealBackendSecretsUseCurrentAuthorisedSettings(t *testing.T) {
	root := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
		t.Setenv(key, root)
	}
	if err := os.MkdirAll(client.ConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	host := client.Host{Name: "box", SSH: "box", Secrets: []string{"TOKEN"}}
	if err := client.SaveHosts(t.Context(), []client.Host{host}); err != nil {
		t.Fatal(err)
	}
	hosts, err := client.LoadHosts()
	if err != nil || len(hosts) != 1 {
		t.Fatalf("saved hosts: %v, %v", hosts, err)
	}
	host = hosts[0]
	if err := client.SetTierContext(t.Context(), host.Name, client.TierOwn, strings.Repeat("a", 32), ""); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "stale-command-must-not-run")
	stale := client.DefaultSettings()
	stale.Secrets = map[string]client.SecretDefinition{"TOKEN": {Run: "echo stale > " + strconv.Quote(marker)}}
	for _, row := range []struct {
		name, text string
		valid      bool
	}{
		{"fresh", "[secrets]\nTOKEN = 'echo current-value'\n", true},
		{"broken", "[secrets]\nTOKEN = [\n", false},
		{"unused invalid", "[secrets]\nTOKEN = 'echo current-value'\nUNUSED = {run = 'echo never-run', kind = 'unknown'}\n", false},
	} {
		t.Run(row.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(client.ConfigDir(), "config.toml"), []byte(row.text), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, operation := range []string{"new", "resume"} {
				ctx := client.WithGrantRequest(t.Context(), client.NewGrantRequest(false, false))
				var got map[string]api.Secret
				var err error
				if operation == "new" {
					got, err = realBackend{}.Secrets(ctx, stale, host, []string{"TOKEN"})
				} else {
					got, err = realBackend{}.SessionSecrets(ctx, stale, host, "abc234de", []string{"TOKEN"})
				}
				if row.valid {
					if err != nil || len(got) != 1 || got["TOKEN"] != "current-value" {
						t.Fatalf("%s fresh fetch: %v, %v", operation, got, err)
					}
				} else {
					var user *client.UserError
					if !errors.As(err, &user) || user.Code != "bad_config" || got != nil {
						t.Fatalf("%s bypassed current invalid settings: %v, %v", operation, got, err)
					}
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatalf("%s ran a stale presentation command: %v", operation, err)
				}
			}
		})
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
	if !hs.m.settings.Agents.Inline || hs.m.settings.Secrets["TOKEN"].Run == "" {
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
