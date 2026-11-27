package tui

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

// oldKeymapNames maps an old [keymap] name to its new one (cli-spec 3.2).
var oldKeymapNames = map[string]string{
	"session: preview": "session: peek",
}

// keymapValue looks up name in km, falling back to any old [keymap] name for it.
func keymapValue(km map[string]any, name string) (any, bool) {
	if v, ok := km[name]; ok {
		return v, true
	}
	for old, cur := range oldKeymapNames {
		if cur == name {
			v, ok := km[old]
			return v, ok
		}
	}
	return nil, false
}

// withKeymap applies the user's [keymap] settings to the registry. A command's
// new keys keep the scope of its first default key.
func withKeymap(cmds []*command, km map[string]any) []*command {
	for _, c := range cmds {
		v, ok := keymapValue(km, c.name())
		if !ok {
			continue
		}
		var keys []string
		switch x := v.(type) {
		case string:
			if x != "" {
				keys = []string{x}
			}
		case []any:
			for _, k := range x {
				if s, ok := k.(string); ok && s != "" {
					keys = append(keys, s)
				}
			}
		case bool:
			// false unbinds; true keeps the defaults
			if x {
				continue
			}
		}
		where := anywhere
		if len(c.keys) > 0 {
			where = c.keys[0].where
		}
		c.keys = nil
		for _, k := range keys {
			c.keys = append(c.keys, binding{key: strings.ToLower(k), where: where})
		}
	}
	return cmds
}

// keymapOnStart shows one notice at startup when [keymap] uses an old name.
func (m *Model) keymapOnStart() tea.Cmd {
	if _, ok := m.settings.Keymap["session: preview"]; !ok || client.OldKeymapPreview == nil {
		return nil
	}
	return m.toastNext(toastWarning, client.OldKeymapPreview.Warning(), "Rename it in config.toml.")
}

// KeyEntry is one line of the effective keymap.
type KeyEntry struct {
	Command string   `json:"command"`
	Keys    []string `json:"keys"`
}

// Keymap returns the effective keymap for the given settings.
func Keymap(s client.Settings) []KeyEntry {
	cmds := withKeymap(registry(), s.Keymap)
	out := make([]KeyEntry, 0, len(cmds))
	for _, c := range cmds {
		e := KeyEntry{Command: c.name()}
		for _, b := range c.keys {
			e.Keys = append(e.Keys, b.key)
		}
		out = append(out, e)
	}
	return out
}

// PaletteAction describes one entry in the launcher's command registry so
// tests can check that the palette and the CLI agree (cli-spec 15.3).
type PaletteAction struct {
	Name         string
	Label        string
	CLI          string
	LauncherOnly string
	Hidden       bool
}

// Palette returns every entry in the launcher's command registry.
func Palette() []PaletteAction {
	cmds := registry()
	out := make([]PaletteAction, len(cmds))
	for i, c := range cmds {
		out[i] = PaletteAction{
			Name:         c.name(),
			Label:        c.label(),
			CLI:          c.cli,
			LauncherOnly: c.launcherOnly,
			Hidden:       c.hidden,
		}
	}
	return out
}

// keyMods are the modifiers of a key, in the order that Bubble Tea names
// them, as in "ctrl+shift+up".
var keyMods = []string{"ctrl", "alt", "shift", "meta", "hyper", "super"}

// keyNames are the names of the keys that are not one character, as
// Bubble Tea names them: "enter", "esc", "space", "pgup", "f1" and the rest.
var keyNames = func() map[string]bool {
	names := map[string]bool{}
	for _, r := range []rune{tea.KeyEnter, tea.KeyTab, tea.KeyBackspace, tea.KeyEscape, tea.KeySpace} {
		names[tea.Key{Code: r}.String()] = true
	}
	for r := tea.KeyUp; r <= tea.KeyIsoLevel5Shift; r++ {
		names[tea.Key{Code: r}.String()] = true
	}
	return names
}()

// CheckKey says why the launcher cannot match k, a key of [keymap], or
// returns nil. The launcher makes each key lower case, and compares it
// with Bubble Tea's name of the key that was pressed: the character that a
// key types, or else its modifiers in Bubble Tea's order and the key's
// name. `config check` uses it.
func CheckKey(k string) error {
	low := strings.ToLower(k)
	key, mods := low, ""
	// "ctrl++" is ctrl and the plus key, and "+" alone is the plus key.
	if i := strings.LastIndex(strings.TrimSuffix(low, "+"), "+"); i >= 0 {
		if i == 0 {
			return fmt.Errorf("%q is not a key: nothing comes before its first \"+\"", k)
		}
		key, mods = low[i+1:], low[:i]
	}
	r, n := utf8.DecodeRuneInString(key)
	if !keyNames[key] && (n != len(key) || r == utf8.RuneError || !unicode.IsPrint(r) || r == ' ') {
		return fmt.Errorf("%q is not a key: %q is neither one character nor a key name such as enter, esc, space, tab, up or f1", k, key)
	}
	if mods == "" {
		if k != low && !keyNames[key] {
			return fmt.Errorf("the launcher reads %q as %q, because it makes each key lower case: write %q, or use ctrl or alt with it", k, low, low)
		}
		return nil
	}
	if err := checkMods(low, key, mods); err != nil {
		return err
	}
	if mods == "shift" && n == len(key) {
		if unicode.IsLetter(r) {
			return fmt.Errorf("the launcher never matches %q: a capital letter arrives as %q, and the launcher makes each key lower case. Use ctrl or alt with %q instead", k, strings.ToUpper(key), key)
		}
		return fmt.Errorf("the launcher never matches %q: shift and a character arrive as the character that they type, so bind that character instead", k)
	}
	return nil
}

// checkMods checks the modifiers of the key k: each one is known, and they
// come in Bubble Tea's order.
func checkMods(k, key, mods string) error {
	at, inOrder := 0, true
	var have []string
	for _, m := range strings.Split(mods, "+") {
		i := slices.Index(keyMods, m)
		if i < 0 {
			return fmt.Errorf("%q is not a key: %q is not a modifier, which is ctrl, alt, shift, meta, hyper or super", k, m)
		}
		inOrder = inOrder && i >= at
		at = i + 1
		if !slices.Contains(have, m) {
			have = append(have, m)
		}
	}
	if !inOrder {
		slices.SortFunc(have, func(a, b string) int { return slices.Index(keyMods, a) - slices.Index(keyMods, b) })
		return fmt.Errorf("the launcher never matches %q, because it names the modifiers in the order ctrl, alt, shift, meta, hyper, super: write %q", k, strings.Join(append(have, key), "+"))
	}
	return nil
}
