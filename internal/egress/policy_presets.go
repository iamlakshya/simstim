package egress

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/pelletier/go-toml/v2"

	"github.com/iamlakshya/simstim/internal/brand"
)

// The built-in network presets, read once from the embedded rules/presets.toml.

type presetsFile struct {
	Presets map[string]presetEntry `toml:"presets"`
}

type presetEntry struct {
	Description    string       `toml:"description"`
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
		dec := toml.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&pf); err != nil {
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

func formatPolicyTOMLError(err error) error {
	var sme *toml.StrictMissingError
	if errors.As(err, &sme) {
		keys := make([]string, 0, len(sme.Errors))
		for _, e := range sme.Errors {
			k := strings.Join(e.Key(), ".")
			if line, col := e.Position(); line > 0 {
				keys = append(keys, fmt.Sprintf("%s (line %d, column %d)", k, line, col))
			} else {
				keys = append(keys, k)
			}
		}
		return fmt.Errorf("egress: unknown policy key %q. Try: remove it or check %s config check", strings.Join(keys, ", "), brand.Name)
	}
	var de *toml.DecodeError
	if errors.As(err, &de) {
		msg := strings.TrimPrefix(de.Error(), "toml: ")
		if line, col := de.Position(); line > 0 {
			return fmt.Errorf("egress: parse policy TOML at line %d, column %d: %s. Try: check the TOML syntax in the session policy", line, col, msg)
		}
		return fmt.Errorf("egress: parse policy TOML: %s. Try: check the TOML syntax in the session policy", msg)
	}
	return fmt.Errorf("egress: parse policy TOML: %w. Try: check the TOML syntax in the session policy", err)
}
