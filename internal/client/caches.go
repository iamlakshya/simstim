package client

import (
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/sandbox"
)

// HostSandbox overrides [sandbox] defaults from config.toml for sessions on
// one host in hosts.toml.
type HostSandbox struct {
	// CPUs caps the CPU quota of new sessions on this host.
	CPUs int `toml:"cpus,omitempty"`
	// Memory caps the memory of new sessions on this host, such as "4G".
	Memory string `toml:"memory,omitempty"`
	// DiskMax sets the soft disk cap for walled sessions on this host, such as "10G".
	DiskMax string `toml:"disk_max,omitempty"`
	// Env sets non-secret environment variables for new sessions on this host.
	Env map[string]string `toml:"env,omitempty"`
	// CopyIn lists laptop paths (or laptop:guest pairs) copied into new
	// sessions on this host at start.
	CopyIn []string `toml:"copy_in,omitempty"`
	// KeepCaches overrides sandbox.keep_caches from config.toml for this host.
	KeepCaches *bool `toml:"keep_caches,omitempty"`
	// Caches adds relative cache paths under /home/agent, or drops one with
	// a leading "!", layered on top of sandbox.caches from config.toml.
	Caches []string `toml:"caches,omitempty"`
}

func boolPtr(v bool) *bool { return &v }

// CheckCacheRules validates a layer's sandbox.caches rules and returns a
// UserError with code "cache_path_invalid" when any rule is invalid or refused.
func CheckCacheRules(rules []string) error {
	if err := sandbox.ValidateCacheRules(rules); err != nil {
		msg, hint := splitCacheError(err.Error())
		return &UserError{
			Msg:  msg,
			Hint: hint,
			Code: "cache_path_invalid",
			Kind: KindUsage,
		}
	}
	return nil
}

// ResolveCacheConfig merges built-in defaults, config.toml, hosts.toml and any
// session overrides into the effective keep flag and ordered rule list.
func ResolveCacheConfig(s Settings, h Host, sessionKeep *bool, sessionRules []string) (keep bool, rules []string, err error) {
	keep = true
	if s.Sandbox.KeepCaches != nil {
		keep = *s.Sandbox.KeepCaches
	}
	if h.Sandbox != nil && h.Sandbox.KeepCaches != nil {
		keep = *h.Sandbox.KeepCaches
	}
	if sessionKeep != nil {
		keep = *sessionKeep
	}

	var hostRules []string
	if h.Sandbox != nil {
		hostRules = h.Sandbox.Caches
	}
	merged := make([]string, 0, len(s.Sandbox.Caches)+len(hostRules)+len(sessionRules))
	merged = append(merged, s.Sandbox.Caches...)
	merged = append(merged, hostRules...)
	merged = append(merged, sessionRules...)
	if err := CheckCacheRules(merged); err != nil {
		return false, nil, err
	}
	if _, err := sandbox.ResolveCaches(keep, merged); err != nil {
		msg, hint := splitCacheError(err.Error())
		return false, nil, &UserError{
			Msg:  msg,
			Hint: hint,
			Code: "cache_path_invalid",
			Kind: KindUsage,
		}
	}
	return keep, slices.Clone(merged), nil
}

// withCacheSettings fills and validates a walled session's kept-cache settings
// from config.toml and hosts.toml (spec 17.5). Unwalled sessions without
// explicit cache fields stay untouched for older helpers.
func (c *Conn) withCacheSettings(a NewArgs) (NewArgs, error) {
	walled := a.Wall != "" && a.Wall != "none"
	if !walled && a.KeepCaches == nil && len(a.Caches) == 0 {
		return a, nil
	}
	s := LoadSettings()
	keep, rules, err := ResolveCacheConfig(s, c.Host, a.KeepCaches, a.Caches)
	if err != nil {
		return a, err
	}
	a.KeepCaches = boolPtr(keep)
	a.Caches = rules
	return a, nil
}

func splitCacheError(raw string) (msg, hint string) {
	if before, after, ok := strings.Cut(raw, ". Try: "); ok {
		return before, after
	}
	return raw, "Check sandbox.caches in config.toml or hosts.toml."
}
