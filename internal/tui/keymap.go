package tui

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/client"
)

// OldKeymapNames maps retired keymap command names to their current names
// during the deprecation window (docs/contributing/cli-spec.md 3 and 15.1).
var OldKeymapNames = map[string]string{
	"session: preview": "session: peek",
}

// UsedOldKeymapNames returns the sorted old keymap command names present in km.
func UsedOldKeymapNames(km map[string]any) []string {
	var used []string
	for old := range OldKeymapNames {
		if _, ok := km[old]; ok {
			used = append(used, old)
		}
	}
	sort.Strings(used)
	return used
}

func lookupKeymap(km map[string]any, name string) (any, bool) {
	if v, ok := km[name]; ok {
		return v, true
	}
	for old, cur := range OldKeymapNames {
		if cur == name {
			if v, ok := km[old]; ok {
				return v, true
			}
		}
	}
	return nil, false
}

// withKeymap applies the user's [keymap] settings to the registry. A command's
// new keys keep the scope of its first default key. A retired command name in
// OldKeymapNames applies when the current name is not set.
func withKeymap(cmds []*command, km map[string]any) []*command {
	for _, c := range cmds {
		v, ok := lookupKeymap(km, c.name())
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

// KeymapCommands returns every launcher command name that [keymap] accepts,
// in sorted order.
func KeymapCommands() []string {
	cmds := registry()
	out := make([]string, 0, len(cmds))
	for _, c := range cmds {
		out = append(out, c.name())
	}
	sort.Strings(out)
	return out
}

// modOrder is the exact order in which ultraviolet / Bubble Tea v2 formats
// modifier prefixes on KeyPressMsg.String().
var modOrder = map[string]int{
	"ctrl":  1,
	"alt":   2,
	"shift": 3,
	"meta":  4,
	"hyper": 5,
	"super": 6,
}

// specialKeys are the non-character key names Bubble Tea v2 emits.
var specialKeys = map[string]bool{
	"enter": true, "tab": true, "backspace": true, "esc": true, "space": true,
	"up": true, "down": true, "left": true, "right": true,
	"home": true, "end": true, "pgup": true, "pgdown": true,
	"insert": true, "delete": true, "begin": true, "find": true, "select": true,
	"f1": true, "f2": true, "f3": true, "f4": true, "f5": true, "f6": true,
	"f7": true, "f8": true, "f9": true, "f10": true, "f11": true, "f12": true,
	"f13": true, "f14": true, "f15": true, "f16": true, "f17": true, "f18": true,
	"f19": true, "f20": true,
}

// CheckKey checks whether raw is a key string that the launcher's keymap can
// parse and match against Bubble Tea v2 key presses (docs/contributing/cli-spec.md 13.2).
func CheckKey(raw string) error {
	if raw == "" {
		return nil // empty string unbinds
	}
	low := strings.ToLower(raw)
	if strings.TrimSpace(low) != low {
		return fmt.Errorf("key %q has leading or trailing spaces", raw)
	}
	mods, base, err := splitKeyMods(raw, low)
	if err != nil {
		return err
	}
	if base == "" {
		return fmt.Errorf("key %q has no base key", raw)
	}
	if specialKeys[base] {
		return nil
	}
	r, size := utf8.DecodeRuneInString(base)
	if size != len(base) || r == utf8.RuneError || !unicode.IsPrint(r) || r == ' ' {
		return fmt.Errorf("unknown key %q", raw)
	}
	// Bubble Tea v2 reports shifted printable characters by their text (e.g. "A"),
	// never as "shift+a", so a shift-only modifier on a printable rune never matches.
	if len(mods) == 1 && mods[0] == "shift" {
		return fmt.Errorf("key %q never matches (shift on a printable key sends its character directly)", raw)
	}
	return nil
}

// splitKeyMods splits a lower-cased key binding like "ctrl+alt+e" or "ctrl++"
// into its modifier prefixes and base key, validating modifier names, duplicates,
// and Bubble Tea v2's canonical modifier order.
func splitKeyMods(raw, low string) (mods []string, base string, err error) {
	rest := low
	lastOrder := 0
	for {
		mod, after, ok := strings.Cut(rest, "+")
		if !ok {
			return mods, rest, nil
		}
		// A bare "+" or a trailing "+" after modifiers (e.g. "ctrl++") is the '+' key.
		if mod == "" && after == "" {
			return mods, "+", nil
		}
		order, isMod := modOrder[mod]
		if !isMod {
			return nil, "", fmt.Errorf("unknown modifier %q in key %q", mod, raw)
		}
		if order <= lastOrder {
			return nil, "", fmt.Errorf("modifiers in %q are out of order or repeated (use ctrl, alt, shift, meta, hyper, super)", raw)
		}
		lastOrder = order
		mods = append(mods, mod)
		if after == "+" {
			return mods, "+", nil
		}
		if after == "" {
			return nil, "", fmt.Errorf("key %q has no base key", raw)
		}
		rest = after
	}
}
