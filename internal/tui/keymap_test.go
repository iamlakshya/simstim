package tui

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
)

func TestWithKeymap(t *testing.T) {
	km := map[string]any{
		"session: delete": []any{"x", "Delete"},
		"session: rename": "",
		"host: add":       false,
	}
	cmds := withKeymap(registry(), km)
	find := func(name string) *command {
		for _, c := range cmds {
			if c.name() == name {
				return c
			}
		}
		t.Fatalf("no command %q", name)
		return nil
	}
	if d := find("session: delete"); len(d.keys) != 2 || d.keys[0].key != "x" || d.keys[1].key != "delete" {
		t.Errorf("delete keys = %+v", d.keys)
	}
	if r := find("session: rename"); len(r.keys) != 0 {
		t.Errorf("rename should be unbound, keys = %+v", r.keys)
	}
	if a := find("host: add"); len(a.keys) != 0 {
		t.Errorf("host add should be unbound, keys = %+v", a.keys)
	}
	if n := find("session: new"); len(n.keys) == 0 {
		t.Error("an untouched command lost its keys")
	}
}

func TestKeymapPeekAndOldPreviewAlias(t *testing.T) {
	known := KeymapCommands()
	if !slices.Contains(known, "session: peek") {
		t.Fatalf("KeymapCommands missing session: peek: %v", known)
	}
	if slices.Contains(known, "session: preview") {
		t.Fatalf("KeymapCommands still contains retired session: preview: %v", known)
	}

	// Retired "session: preview" still rebinds "session: peek".
	s := client.Settings{Keymap: map[string]any{"session: preview": "p"}}
	var peekKeys []string
	for _, e := range Keymap(s) {
		if e.Command == "session: peek" {
			peekKeys = e.Keys
		}
	}
	if !slices.Equal(peekKeys, []string{"p"}) {
		t.Errorf("old alias keys = %v, want [p]", peekKeys)
	}
	if used := UsedOldKeymapNames(s.Keymap); !slices.Equal(used, []string{"session: preview"}) {
		t.Errorf("UsedOldKeymapNames = %v", used)
	}

	// Explicit "session: peek" takes precedence over "session: preview".
	s.Keymap["session: peek"] = "k"
	peekKeys = nil
	for _, e := range Keymap(s) {
		if e.Command == "session: peek" {
			peekKeys = e.Keys
		}
	}
	if !slices.Equal(peekKeys, []string{"k"}) {
		t.Errorf("new name precedence keys = %v, want [k]", peekKeys)
	}

	// The palette matches "peek" and does not match "preview".
	hs := sessions(t, 80, 24)
	p := &palette{in: newInput("peek", "")}
	items := p.items(hs.m)
	if len(items) != 1 || items[0].c.name() != "session: peek" {
		t.Errorf("palette filter 'peek' matched %d items", len(items))
	}
	pOld := &palette{in: newInput("preview", "")}
	if got := pOld.items(hs.m); len(got) != 0 {
		t.Errorf("palette filter 'preview' should match nothing, got %d items", len(got))
	}
}

func TestRenderPlainScreen(t *testing.T) {
	raw := []byte("\x1b[31mhello world\x1b[0m   \r\n\x1b]8;;https://example.invalid\x07link\x1b]8;;\x07  \r\n日本語\u202e\r\n")
	lines := RenderPlainScreen(raw, 20, 6)
	if len(lines) != 6 {
		t.Fatalf("len(lines) = %d, want 6", len(lines))
	}
	if lines[0] != "hello world" {
		t.Errorf("line 0 = %q, want %q", lines[0], "hello world")
	}
	if lines[1] != "link" {
		t.Errorf("line 1 = %q, want %q", lines[1], "link")
	}
	if lines[2] != "日本語" {
		t.Errorf("line 2 = %q, want %q", lines[2], "日本語")
	}
	for i, l := range lines {
		if strings.ContainsRune(l, '\x1b') {
			t.Errorf("line %d contains escape sequence: %q", i, l)
		}
		if strings.TrimRight(l, " \t") != l {
			t.Errorf("line %d has trailing whitespace: %q", i, l)
		}
	}
	if lines[3] != "" || lines[4] != "" || lines[5] != "" {
		t.Errorf("empty trailing rows = %q", lines[3:])
	}
}

func TestOldKeymapPreviewToastNotice(t *testing.T) {
	s := client.DefaultSettings()
	s.Keymap = map[string]any{"session: preview": "p"}
	be := &settingsFakeBackend{
		fakeBackend: newFakeBackend(),
		settings:    s,
	}
	m := New(context.Background(), be, Options{Motion: false})
	t.Cleanup(m.Close)
	_ = m.Init()
	if !strings.Contains(m.configWarn, `"session: preview" is now "session: peek"`) {
		t.Fatalf("configWarn = %q, want old keymap notice", m.configWarn)
	}
	hasToast(t, m, `"session: preview" is now "session: peek" in [keymap].`)
}
