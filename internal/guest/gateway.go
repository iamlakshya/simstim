package guest

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/paths"
)

// GatewayBaseURL returns the plain-HTTP model gateway base URL that an agent
// inside the sandbox uses, e.g. "http://127.0.0.1:3128/gw/anthropic" for
// Claude Code or "http://127.0.0.1:3128/gw/openai/v1" for Codex.
func GatewayBaseURL(proxyURL string, gw *agents.ModelGateway) string {
	if gw == nil || gw.Route == "" {
		return ""
	}
	base := strings.TrimRight(proxyURL, "/") + egress.GatewayPathPrefix + strings.Trim(gw.Route, "/")
	if gw.BaseURLPath != "" {
		base += "/" + strings.TrimLeft(gw.BaseURLPath, "/")
	}
	return base
}

// GatewayPlaceholder returns a non-secret stand-in string placed in the
// sandbox's credential environment variable so the agent sends requests to the
// gateway route without ever holding the real key. The egress process strips
// this placeholder and injects the real key over verified TLS.
func GatewayPlaceholder(envName string) string {
	return brand.Name + "-gateway-placeholder-" + strings.ToLower(strings.TrimSpace(envName))
}

// GatewayEnv returns the sandbox environment variables that point agent at its
// model gateway route (such as ANTHROPIC_BASE_URL for Claude Code) and set a
// non-secret placeholder credential variable so the real key never enters the
// sandbox. All settings are read from agent.ModelGateway (no name branching).
func GatewayEnv(proxyURL string, agent agents.Agent, boundSecretEnv string) map[string]string {
	gw := agent.ModelGateway
	if gw == nil {
		return nil
	}
	baseURL := GatewayBaseURL(proxyURL, gw)
	if baseURL == "" {
		return nil
	}
	env := map[string]string{}
	if gw.BaseURLEnv != "" {
		env[gw.BaseURLEnv] = baseURL
	}
	credEnv := strings.TrimSpace(boundSecretEnv)
	if credEnv == "" {
		credEnv = gw.DummyEnv
	}
	if credEnv != "" {
		env[credEnv] = GatewayPlaceholder(credEnv)
	}
	return env
}

// WriteGatewayConfig renders and writes agent.ModelGateway.ConfigTmpl (for
// example Codex's .codex/config.toml with supports_websockets = false) under
// homeDir (or the directory named by agent.ModelGateway.ConfigEnv in env).
// It returns the written path, or "" when the agent needs no config file.
func WriteGatewayConfig(homeDir string, env map[string]string, proxyURL string, agent agents.Agent, boundSecretEnv string) (string, error) {
	gw := agent.ModelGateway
	if gw == nil || gw.ConfigFile == "" || gw.ConfigTmpl == "" {
		return "", nil
	}
	baseURL := GatewayBaseURL(proxyURL, gw)
	if baseURL == "" {
		return "", nil
	}
	envKey := strings.TrimSpace(boundSecretEnv)
	if envKey == "" {
		envKey = gw.DummyEnv
	}
	target := filepath.Join(homeDir, gw.ConfigFile)
	if gw.ConfigEnv != "" {
		if override := strings.TrimSpace(env[gw.ConfigEnv]); override != "" {
			target = filepath.Join(override, filepath.Base(gw.ConfigFile))
		}
	}
	rendered := strings.ReplaceAll(gw.ConfigTmpl, "{base_url}", baseURL)
	rendered = strings.ReplaceAll(rendered, "{env_key}", envKey)

	dir := filepath.Clean(filepath.Dir(target))
	base := filepath.Base(target)
	if base == "." || base == ".." || strings.ContainsRune(base, filepath.Separator) {
		return "", fmt.Errorf("guest gateway: invalid config file name %q. Try: check agent ModelGateway.ConfigFile", gw.ConfigFile)
	}
	if err := paths.EnsurePrivateDir(dir); err != nil {
		return "", fmt.Errorf("guest gateway: create config folder for %s: %w. Try: check sandbox home permissions", target, err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", fmt.Errorf("guest gateway: open config folder %s: %w. Try: check sandbox home permissions", dir, err)
	}
	defer func() { _ = root.Close() }()
	tmpName := base + ".tmp"
	if err := root.WriteFile(tmpName, []byte(rendered), 0o600); err != nil {
		return "", fmt.Errorf("guest gateway: write %s: %w. Try: check sandbox home permissions", target, err)
	}
	if err := root.Rename(tmpName, base); err != nil {
		_ = root.Remove(tmpName)
		return "", fmt.Errorf("guest gateway: install %s: %w. Try: check sandbox home permissions", target, err)
	}
	return filepath.Join(dir, base), nil
}

// applyStartGateway wires an agent's ModelGateway environment variables and
// per-session config file (such as Codex's /home/agent/.codex/config.toml) plus
// any non-secret placeholders for custom bound secrets before starting the
// guest holder. Nothing touches the host user's own config files.
func applyStartGateway(a StartReq, env []string) ([]string, error) {
	if a.ProxyURL == "" {
		return env, nil
	}
	setKeys := map[string]bool{}
	if agent, ok := agents.Find(a.Agent); ok && agent.ModelGateway != nil {
		gwEnv := GatewayEnv(a.ProxyURL, agent, a.BoundSecretEnv)
		keys := make([]string, 0, len(gwEnv))
		for k := range gwEnv {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			env = withEnvVar(env, k, gwEnv[k])
			setKeys[k] = true
		}
		envMap := parseEnvSlice(env)
		if homeDir := envMap["HOME"]; homeDir != "" {
			if _, err := WriteGatewayConfig(homeDir, envMap, a.ProxyURL, agent, a.BoundSecretEnv); err != nil {
				return nil, err
			}
		}
	}
	for _, kv := range a.BoundPlaceholders {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" && !setKeys[k] {
			env = withEnvVar(env, k, v)
		}
	}
	return env, nil
}
