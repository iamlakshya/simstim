package tui

import (
	"strings"

	"github.com/iamlakshya/simstim/internal/client"
)

// withKeymap applies the user's [keymap] settings to the registry. A command's
// new keys keep the scope of its first default key.
func withKeymap(cmds []*command, km map[string]any) []*command {
	for _, c := range cmds {
		v, ok := km[c.name()]
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
