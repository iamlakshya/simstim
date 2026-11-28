package client

import (
	"errors"
	"fmt"

	tomlv2 "github.com/pelletier/go-toml/v2"
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
	if s.UI.Motion == "" {
		s.UI.Motion = d.UI.Motion
	}
	if s.Attach.DetachKey == "" {
		s.Attach.DetachKey = d.Attach.DetachKey
	}
	if s.Attach.ImagePasteKey == "" {
		s.Attach.ImagePasteKey = d.Attach.ImagePasteKey
	}
	if s.Attach.Scrollback == "" {
		s.Attach.Scrollback = d.Attach.Scrollback
	}
	if s.Sessions.MemoryMax == "" {
		s.Sessions.MemoryMax = d.Sessions.MemoryMax
	}
	if s.Sandbox.Paste == "" {
		s.Sandbox.Paste = d.Sandbox.Paste
	}
	if s.Sandbox.PackageManagers == "" {
		s.Sandbox.PackageManagers = d.Sandbox.PackageManagers
	}
	if s.Sandbox.KeepCaches == nil {
		s.Sandbox.KeepCaches = d.Sandbox.KeepCaches
	}
	if s.Network.MinReleaseAge == "" {
		s.Network.MinReleaseAge = d.Network.MinReleaseAge
	}
	if s.Network.UploadBudget == "" {
		s.Network.UploadBudget = d.Network.UploadBudget
	}
	return s, ""
}
