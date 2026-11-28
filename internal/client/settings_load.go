package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	tomlv2 "github.com/pelletier/go-toml/v2"

	"github.com/iamlakshya/simstim/internal/brand"
)

// LoadSettings reads the settings, with defaults for anything missing. If
// config.toml is unreadable, broken or defines case-folded aliases of one
// field, it returns the defaults. Callers that want the warning use
// LoadSettingsChecked.
func LoadSettings() Settings {
	s, _ := LoadSettingsChecked()
	return s
}

// LoadSettingsChecked reads config.toml once as a bounded regular file and
// returns the effective settings plus a warning when the file could not be
// used (docs/contributing/cli-spec.md 13.4).
func LoadSettingsChecked() (Settings, string) {
	d := DefaultSettings()
	b, exists, err := ReadConfigFile(settingsPath())
	if err != nil {
		var re *ConfigReadError
		if errors.As(err, &re) && re.Code == "too_large" {
			return d, fmt.Sprintf("config.toml %s, so its settings are not used", re.Msg)
		}
		return d, "config.toml cannot be read, so its settings are not used"
	}
	if !exists {
		return d, ""
	}
	var s Settings
	ins, err := DecodeConfigLoose(settingsPath(), b, &s)
	if err != nil {
		var re *ConfigReadError
		if errors.As(err, &re) && re.Code == "too_large" {
			return d, fmt.Sprintf("config.toml %s, so its settings are not used", re.Msg)
		}
		var de *tomlv2.DecodeError
		if errors.As(err, &de) {
			if line, _ := de.Position(); line > 0 {
				return d, fmt.Sprintf("config.toml has an error on line %d, so its settings are not used", line)
			}
		}
		return d, "config.toml has an error, so its settings are not used"
	}
	if len(ins.Aliases) > 0 {
		a := ins.Aliases[0]
		scope := "at the top level"
		if a.Table != "" {
			scope = "in [" + a.Table + "]"
		}
		return d, fmt.Sprintf("config.toml names %q and %q for the same setting %s, so its settings are not used", a.First, a.Second, scope)
	}
	applySettingsDefaults(&s, d)
	return s, ""
}

func applySettingsDefaults(s *Settings, d Settings) {
	fill := func(dst *string, def string) {
		if *dst == "" {
			*dst = def
		}
	}
	fill(&s.UI.Motion, d.UI.Motion)
	fill(&s.Attach.DetachKey, d.Attach.DetachKey)
	fill(&s.Attach.ImagePasteKey, d.Attach.ImagePasteKey)
	fill(&s.Attach.Scrollback, d.Attach.Scrollback)
	fill(&s.Sessions.MemoryMax, d.Sessions.MemoryMax)
	fill(&s.Sandbox.Paste, d.Sandbox.Paste)
	fill(&s.Sandbox.PackageManagers, d.Sandbox.PackageManagers)
	if s.Sandbox.KeepCaches == nil {
		s.Sandbox.KeepCaches = d.Sandbox.KeepCaches
	}
	fill(&s.Sandbox.CopyMax, d.Sandbox.CopyMax)
	fill(&s.Sandbox.SeedMax, d.Sandbox.SeedMax)
	fill(&s.Sandbox.System, d.Sandbox.System)
	fill(&s.Network.MinReleaseAge, d.Network.MinReleaseAge)
	fill(&s.Network.UploadBudget, d.Network.UploadBudget)
	fill(&s.Images.Fetch, d.Images.Fetch)
}

// UserError is an error with a suggested next step: the message, and the
// "Try:" line (docs/contributing/cli-spec.md, section 4).
type UserError struct {
	Msg  string
	Hint string
	// Code names the error, such as host_not_found. A host sends its own
	// codes, and the client acts on some of them, such as a repository's
	// setup script that needs the user's answer. Data carries what the
	// client needs for that.
	Code string
	Data json.RawMessage
	// Kind sets the exit code. The zero Kind is KindFailed.
	Kind Kind
}

func (e *UserError) Error() string { return e.Msg }

// SplitTarget splits "host/session" into its parts.
func SplitTarget(t string) (host, session string, err error) {
	host, session, ok := strings.Cut(t, "/")
	if !ok || host == "" || session == "" {
		return "", "", &UserError{Msg: fmt.Sprintf("%q is not host/session", t), Hint: brand.Name + " ls", Code: "bad_address", Kind: KindUsage}
	}
	return host, session, nil
}
