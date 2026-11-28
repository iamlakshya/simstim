package client

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCacheSettingsDefaultsAndLayering(t *testing.T) {
	d := DefaultSettings()
	if d.Sandbox.KeepCaches == nil || !*d.Sandbox.KeepCaches {
		t.Fatalf("default sandbox.keep_caches = %v, want true", d.Sandbox.KeepCaches)
	}
	if len(d.Sandbox.Caches) != 0 {
		t.Fatalf("default sandbox.caches = %v, want empty", d.Sandbox.Caches)
	}

	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	dir := ConfigDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfgTOML := `
[sandbox]
keep_caches = true
caches = ["!.gradle/caches", ".cache/custom"]
`
	hostsTOML := `
[[host]]
name = "box-on"
ssh = "dev@box-on"
[host.sandbox]
caches = ["!.m2/repository", ".cache/host-tool"]

[[host]]
name = "box-off"
ssh = "dev@box-off"
[host.sandbox]
keep_caches = false
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(cfgTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hosts.toml"), []byte(hostsTOML), 0o600); err != nil {
		t.Fatal(err)
	}

	s := LoadSettings()
	if s.Sandbox.KeepCaches == nil || !*s.Sandbox.KeepCaches {
		t.Fatalf("loaded keep_caches = %v, want true", s.Sandbox.KeepCaches)
	}
	hosts, err := LoadHosts()
	if err != nil {
		t.Fatalf("LoadHosts: %v", err)
	}
	if len(hosts) != 2 {
		t.Fatalf("len(hosts) = %d, want 2", len(hosts))
	}

	// Host with caches override layers on top of config.toml.
	cOn := &Conn{Host: hosts[0]}
	unwalled, err := cOn.withCacheSettings(NewArgs{Wall: "none"})
	if err != nil {
		t.Fatalf("withCacheSettings(none): %v", err)
	}
	if unwalled.KeepCaches != nil || len(unwalled.Caches) != 0 {
		t.Fatalf("unwalled session got cache settings: %+v", unwalled)
	}

	walled, err := cOn.withCacheSettings(NewArgs{Wall: "process", Caches: []string{".cache/session"}})
	if err != nil {
		t.Fatalf("withCacheSettings(process): %v", err)
	}
	if walled.KeepCaches == nil || !*walled.KeepCaches {
		t.Fatalf("walled.KeepCaches = %v, want true", walled.KeepCaches)
	}
	wantRules := []string{"!.gradle/caches", ".cache/custom", "!.m2/repository", ".cache/host-tool", ".cache/session"}
	if !slices.Equal(walled.Caches, wantRules) {
		t.Fatalf("walled.Caches = %v, want %v", walled.Caches, wantRules)
	}

	// Host with keep_caches = false overrides config.toml's true.
	cOff := &Conn{Host: hosts[1]}
	walledOff, err := cOff.withCacheSettings(NewArgs{Wall: "process"})
	if err != nil {
		t.Fatalf("withCacheSettings(box-off): %v", err)
	}
	if walledOff.KeepCaches == nil || *walledOff.KeepCaches {
		t.Fatalf("walledOff.KeepCaches = %v, want false", walledOff.KeepCaches)
	}

	// Session override can turn keep_caches back on even when host turned it off.
	walledReOn, err := cOff.withCacheSettings(NewArgs{Wall: "process", KeepCaches: boolPtr(true)})
	if err != nil {
		t.Fatalf("withCacheSettings(box-off, session true): %v", err)
	}
	if walledReOn.KeepCaches == nil || !*walledReOn.KeepCaches {
		t.Fatalf("walledReOn.KeepCaches = %v, want true", walledReOn.KeepCaches)
	}

	// Executable path such as .cargo/bin fails with cache_path_invalid.
	_, err = cOn.withCacheSettings(NewArgs{Wall: "process", Caches: []string{".cargo/bin"}})
	var ue *UserError
	if !errors.As(err, &ue) || ue.Code != "cache_path_invalid" || ue.Kind != KindUsage {
		t.Fatalf("withCacheSettings(.cargo/bin) = %#v, want UserError{Code:cache_path_invalid, Kind:KindUsage}", err)
	}
	if ue.Hint == "" || !strings.Contains(ue.Msg, ".cargo/bin") {
		t.Errorf("unexpected error text: msg=%q hint=%q", ue.Msg, ue.Hint)
	}
}

func TestLoadSettingsKeepCachesFalsePreserved(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	dir := ConfigDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[sandbox]\nkeep_caches = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := LoadSettings()
	if s.Sandbox.KeepCaches == nil || *s.Sandbox.KeepCaches {
		t.Fatalf("loaded keep_caches = %v, want false", s.Sandbox.KeepCaches)
	}
}
