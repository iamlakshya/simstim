package egress

import (
	"fmt"
	"sync"

	"github.com/BurntSushi/toml"
)

// The built-in network presets, read once from the embedded rules/presets.toml.

type presetsFile struct {
	Presets map[string]presetEntry `toml:"presets"`
}

type presetEntry struct {
	AllowAllPublic bool         `toml:"allow_all_public"`
	Entries        []presetHost `toml:"entries"`
}

type presetHost struct {
	Host   string `toml:"host"`
	Source string `toml:"source"`
}

var (
	loadPresetsOnce sync.Once
	loadedPresets   map[Preset][]rule
	errLoadPresets  error
)

func builtinPresets() (map[Preset][]rule, error) {
	loadPresetsOnce.Do(func() {
		raw, err := rulesFS.ReadFile("rules/presets.toml")
		if err != nil {
			errLoadPresets = fmt.Errorf("egress: read embedded presets: %w", err)
			return
		}
		var pf presetsFile
		if _, err := toml.Decode(string(raw), &pf); err != nil {
			errLoadPresets = fmt.Errorf("egress: decode embedded presets: %w", err)
			return
		}
		out := make(map[Preset][]rule, len(pf.Presets))
		for name, pe := range pf.Presets {
			rules := make([]rule, 0, len(pe.Entries))
			for _, e := range pe.Entries {
				r, err := parseRule(e.Host, false)
				if err != nil {
					errLoadPresets = fmt.Errorf("egress: preset %q entry %q: %w", name, e.Host, err)
					return
				}
				rules = append(rules, r)
			}
			out[Preset(name)] = rules
		}
		loadedPresets = out
	})
	return loadedPresets, errLoadPresets
}
