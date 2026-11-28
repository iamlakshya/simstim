package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

type settingsFakeBackend struct {
	*fakeBackend
	settings client.Settings
	warn     string
}

func (b *settingsFakeBackend) Settings() client.Settings { return b.settings }
func (b *settingsFakeBackend) LoadSettings() (client.Settings, string) {
	return b.settings, b.warn
}

func TestCheckKey(t *testing.T) {
	valid := []string{
		"", "a", "?", "/", "+", "1",
		"enter", "space", "tab", "esc", "backspace", "f2", "pgup", "pgdown",
		"ctrl+o", "alt+e", "shift+tab", "ctrl+shift+a", "ctrl+alt+shift+x", "ctrl++",
	}
	for _, k := range valid {
		if err := CheckKey(k); err != nil {
			t.Errorf("CheckKey(%q) = %v, want nil", k, err)
		}
	}

	invalid := []string{
		"ctrl+nothing",
		"alt+ctrl+e",
		"ctrl+ctrl+a",
		"ctrl+",
		"foo+a",
		"shift+a",
		"notakey",
		" a",
		"a ",
	}
	for _, k := range invalid {
		if err := CheckKey(k); err == nil {
			t.Errorf("CheckKey(%q) = nil, want error", k)
		}
	}
}

func TestKeymapCommandsIncludesKnownCommands(t *testing.T) {
	cmds := KeymapCommands()
	if len(cmds) < 10 {
		t.Fatalf("KeymapCommands returned only %d commands", len(cmds))
	}
	want := map[string]bool{"session: delete": false, "view: search": false, brand.Name + ": quit": false}
	for _, c := range cmds {
		if _, ok := want[c]; ok {
			want[c] = true
		}
	}
	for k, seen := range want {
		if !seen {
			t.Errorf("KeymapCommands missing %q", k)
		}
	}
}

func TestLauncherBrokenConfigKeepsGoodSettingsAndOrdersReloads(t *testing.T) {
	good := client.DefaultSettings()
	good.UI.Motion = "on"
	good.Attach.DetachKey = "ctrl+a"

	fb := &fakeBackend{}
	be := &settingsFakeBackend{
		fakeBackend: fb,
		settings:    client.DefaultSettings(),
		warn:        "config.toml has an error on line 2, so its settings are not used",
	}

	// Startup with a broken config.toml shows a warning toast pointing to config check.
	m := New(context.Background(), be, Options{Motion: false})
	_ = m.Init()
	hasToast(t, m, "Config.toml has an error on line 2, so its settings are not used.")
	if len(m.toasts.shown) == 0 || m.toasts.shown[len(m.toasts.shown)-1].hint != brand.Name+" config check" {
		t.Fatalf("startup config warning toast = %+v", m.toasts.shown)
	}

	// A good read applies the new settings and updates motion.
	be.settings, be.warn = good, ""
	seq1 := m.nextSettingsSeq()
	if cmd := m.applySettings(good, "", seq1); cmd != nil {
		t.Fatalf("good settings apply returned unexpected toast cmd")
	}
	if m.settings.Attach.DetachKey != "ctrl+a" || !m.motion || m.configWarn != "" {
		t.Fatalf("after good reload: detach=%q motion=%v warn=%q", m.settings.Attach.DetachKey, m.motion, m.configWarn)
	}

	// A broken edit keeps the previously good settings and warns once.
	brokenWarn := "config.toml has an error on line 5, so its settings are not used"
	seq2 := m.nextSettingsSeq()
	cmd := m.applySettings(client.DefaultSettings(), brokenWarn, seq2)
	if cmd == nil {
		t.Fatal("broken reload should return warning toast cmd")
	}
	if m.settings.Attach.DetachKey != "ctrl+a" {
		t.Fatalf("broken reload overwrote good settings: detach=%q", m.settings.Attach.DetachKey)
	}
	// Repeating the same warning while still broken does not spam toasts.
	seq3 := m.nextSettingsSeq()
	if cmd := m.applySettings(client.DefaultSettings(), brokenWarn, seq3); cmd != nil {
		t.Fatal("repeated identical broken warning should not re-toast")
	}

	// An older reload (seq < m.settingsSeq) is ignored even if it has good settings.
	older := client.DefaultSettings()
	older.Attach.DetachKey = "ctrl+z"
	if cmd := m.applySettings(older, "", seq1); cmd != nil {
		t.Fatal("stale reload returned cmd")
	}
	if m.settings.Attach.DetachKey != "ctrl+a" || !strings.Contains(m.configWarn, "line 5") {
		t.Fatalf("stale reload corrupted state: detach=%q warn=%q", m.settings.Attach.DetachKey, m.configWarn)
	}
}
