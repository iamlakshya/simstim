package guest

import (
	"path/filepath"
	"slices"
)

const pnpmStoreCacheRel = ".local/share/pnpm/store"

// applyStartCaches configures guest home environment variables needed so tools
// use their kept bind-mounted cache directories when /home/agent is a tmpfs
// (isolation spec 17.5). User overrides in sandbox.env always win.
func applyStartCaches(a StartReq, baseEnv []string) []string {
	if len(a.CacheDirs) == 0 {
		return baseEnv
	}
	homeDir := envLookup(baseEnv, "HOME")
	if homeDir == "" {
		return baseEnv
	}
	userEnv := parseEnvSlice(a.UserEnv)
	env := baseEnv
	if slices.Contains(a.CacheDirs, pnpmStoreCacheRel) {
		// Inside a process or gVisor wall, /home/agent is a tmpfs while /work
		// and the kept cache bind mount sit on the host filesystem. Without an
		// explicit store dir, pnpm sees different filesystem device IDs between
		// $HOME and /work and falls back to <project>/node_modules/.pnpm-store,
		// losing the cache on reinstall or restart.
		storeDir := filepath.Join(homeDir, filepath.FromSlash(pnpmStoreCacheRel))
		if _, overridden := userEnv["pnpm_config_store_dir"]; !overridden {
			env = withEnvVar(env, "pnpm_config_store_dir", storeDir)
		}
		if _, overridden := userEnv["npm_config_store_dir"]; !overridden {
			env = withEnvVar(env, "npm_config_store_dir", storeDir)
		}
	}
	return env
}
