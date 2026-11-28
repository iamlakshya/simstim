package client

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackageSettingsDefaultsAndValidation(t *testing.T) {
	d := DefaultSettings()
	if d.Sandbox.PackageManagers != "guarded" {
		t.Errorf("default sandbox.package_managers = %q, want guarded", d.Sandbox.PackageManagers)
	}
	if d.Network.MinReleaseAge != "7d" {
		t.Errorf("default network.min_release_age = %q, want 7d", d.Network.MinReleaseAge)
	}
	if err := ValidatePackageSettings(d); err != nil {
		t.Errorf("ValidatePackageSettings(default) = %v, want nil", err)
	}

	// network.min_release_age = "90m" fails with release_age_invalid.
	_, _, err := CheckMinReleaseAge("90m")
	var ue *UserError
	if !errors.As(err, &ue) || ue.Code != "release_age_invalid" || ue.Kind != KindUsage {
		t.Fatalf("CheckMinReleaseAge(90m) = %#v, want UserError{Code:release_age_invalid, Kind:KindUsage}", err)
	}
	if !strings.Contains(ue.Msg, `network.min_release_age is "90m"`) || !strings.Contains(ue.Hint, `network.min_release_age = "7d"`) {
		t.Errorf("unexpected release_age_invalid text: msg=%q hint=%q", ue.Msg, ue.Hint)
	}

	// sandbox.package_managers = "on" fails with package_level_invalid.
	_, err = CheckPackageManagers("on")
	ue = nil
	if !errors.As(err, &ue) || ue.Code != "package_level_invalid" || ue.Kind != KindUsage {
		t.Fatalf("CheckPackageManagers(on) = %#v, want UserError{Code:package_level_invalid, Kind:KindUsage}", err)
	}
	if !strings.Contains(ue.Msg, `sandbox.package_managers is "on"`) || !strings.Contains(ue.Hint, `sandbox.package_managers = "guarded"`) {
		t.Errorf("unexpected package_level_invalid text: msg=%q hint=%q", ue.Msg, ue.Hint)
	}
}

func TestLoadSettingsAndWithPackageSettings(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	dir := ConfigDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw := `
[sandbox]
package_managers = "strict"

[sandbox.env]
GOPROXY = "https://corp.example/go"
npm_config_ignore_scripts = "false"

[network]
min_release_age = "14d"
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}

	s := LoadSettings()
	if s.Sandbox.PackageManagers != "strict" || s.Network.MinReleaseAge != "14d" {
		t.Fatalf("loaded = (%q, %q), want (strict, 14d)", s.Sandbox.PackageManagers, s.Network.MinReleaseAge)
	}
	if s.Sandbox.Env["GOPROXY"] != "https://corp.example/go" {
		t.Fatalf("sandbox.env = %+v", s.Sandbox.Env)
	}

	// Unwalled sessions with no explicit package settings stay untouched.
	unwalled, err := withPackageSettings(NewArgs{Wall: "none"})
	if err != nil {
		t.Fatalf("withPackageSettings(none): %v", err)
	}
	if unwalled.PackageManagers != "" || unwalled.MinReleaseAge != "" || len(unwalled.SandboxEnv) != 0 {
		t.Errorf("unwalled session got package settings: %+v", unwalled)
	}

	// Walled sessions inherit config.toml defaults and merge SandboxEnv (caller wins).
	walled, err := withPackageSettings(NewArgs{
		Wall:       "process",
		SandboxEnv: map[string]string{"GOPROXY": "https://override.example/go"},
	})
	if err != nil {
		t.Fatalf("withPackageSettings(process): %v", err)
	}
	if walled.PackageManagers != "strict" || walled.MinReleaseAge != "14d" {
		t.Errorf("walled = (%q, %q), want (strict, 14d)", walled.PackageManagers, walled.MinReleaseAge)
	}
	if walled.SandboxEnv["GOPROXY"] != "https://override.example/go" || walled.SandboxEnv["npm_config_ignore_scripts"] != "false" {
		t.Errorf("walled.SandboxEnv = %+v", walled.SandboxEnv)
	}
}
