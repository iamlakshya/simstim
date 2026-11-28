package guest

import (
	"testing"
)

func TestApplyStartCachesPnpmStoreDir(t *testing.T) {
	base := []string{"HOME=/home/agent", "PATH=/usr/bin:/bin"}

	// When .local/share/pnpm/store is kept, guest init configures pnpm store env vars.
	got := applyStartCaches(StartReq{
		CacheDirs: []string{".npm/_cacache", ".local/share/pnpm/store"},
	}, base)
	env := parseEnvSlice(got)
	want := "/home/agent/.local/share/pnpm/store"
	if env["pnpm_config_store_dir"] != want {
		t.Errorf("pnpm_config_store_dir = %q, want %q", env["pnpm_config_store_dir"], want)
	}
	if env["npm_config_store_dir"] != want {
		t.Errorf("npm_config_store_dir = %q, want %q", env["npm_config_store_dir"], want)
	}

	// When pnpm store is dropped (or caches disabled), store env vars are not set.
	dropped := applyStartCaches(StartReq{
		CacheDirs: []string{".npm/_cacache"},
	}, base)
	envDropped := parseEnvSlice(dropped)
	if _, ok := envDropped["pnpm_config_store_dir"]; ok {
		t.Errorf("pnpm_config_store_dir set when store cache was dropped: %v", envDropped)
	}
	if _, ok := envDropped["npm_config_store_dir"]; ok {
		t.Errorf("npm_config_store_dir set when store cache was dropped: %v", envDropped)
	}

	// Explicit sandbox.env overrides win over applyStartCaches.
	overridden := applyStartCaches(StartReq{
		CacheDirs: []string{".local/share/pnpm/store"},
		UserEnv:   []string{"pnpm_config_store_dir=/custom/store"},
	}, append(base, "pnpm_config_store_dir=/custom/store"))
	envOver := parseEnvSlice(overridden)
	if envOver["pnpm_config_store_dir"] != "/custom/store" {
		t.Errorf("sandbox.env override lost: pnpm_config_store_dir = %q", envOver["pnpm_config_store_dir"])
	}
}
