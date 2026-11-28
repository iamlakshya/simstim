package guest

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/paths"
)

// SubscriptionGatewayURLs returns the provider base URL (such as
// http://127.0.0.1:3128/gw/chatgpt/backend-api/codex) and secondary root URL
// (such as http://127.0.0.1:3128/gw/chatgpt/backend-api) for sub.
func SubscriptionGatewayURLs(proxyURL string, sub *agents.SubscriptionGateway) (baseURL, rootURL string) {
	if sub == nil || sub.Route == "" {
		return "", ""
	}
	origin := strings.TrimRight(proxyURL, "/") + egress.GatewayPathPrefix + strings.Trim(sub.Route, "/")
	baseURL = origin
	if sub.BaseURLPath != "" {
		baseURL += "/" + strings.TrimLeft(sub.BaseURLPath, "/")
	}
	rootURL = origin
	if sub.RootURLPath != "" {
		rootURL += "/" + strings.TrimLeft(sub.RootURLPath, "/")
	}
	return baseURL, rootURL
}

// CodexPlaceholderAuthJSON renders a non-secret placeholder auth.json for
// Codex's ChatGPT sign-in mode inside the sandbox (decision 0059). Every token
// is a non-secret placeholder with a far-future exp claim so Codex never tries
// to refresh directly from inside the sandbox; the egress gateway strips and
// replaces both Authorization and Chatgpt-Account-Id on the trusted side.
func CodexPlaceholderAuthJSON(now time.Time) []byte {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	b64u := func(v any) string {
		raw, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	hdr := b64u(map[string]string{"alg": "RS256", "typ": "JWT"})
	exp := now.Add(3650 * 24 * time.Hour).Unix()
	idPayload := b64u(map[string]any{
		"sub":   "user-placeholder",
		"email": "sandbox@example.invalid",
		"exp":   exp,
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acct_placeholder",
			"chatgpt_plan_type":  "plus",
			"chatgpt_user_id":    "user-placeholder",
		},
	})
	accPayload := b64u(map[string]any{
		"sub": "user-placeholder",
		"exp": exp,
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acct_placeholder",
			"chatgpt_plan_type":  "plus",
		},
	})
	doc := map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"tokens": map[string]any{
			"id_token":      hdr + "." + idPayload + ".placeholder",
			"access_token":  hdr + "." + accPayload + ".placeholder",
			"refresh_token": brand.Name + "-gateway-placeholder-codex-refresh",
			"account_id":    "acct_placeholder",
		},
		"last_refresh": now.UTC().Format(time.RFC3339),
	}
	out, _ := json.MarshalIndent(doc, "", "  ")
	return append(out, '\n')
}

// WriteSubscriptionGatewayConfig writes the sandbox's local provider config
// (pointing at /gw/<route>) and non-secret placeholder auth file under homeDir
// (or the directory named by sub.ConfigEnv). Host user files are never touched.
func WriteSubscriptionGatewayConfig(
	homeDir string,
	env map[string]string,
	proxyURL string,
	agent agents.Agent,
) (configPath, authPath string, err error) {
	sub := agent.SubscriptionGateway
	if sub == nil || sub.ConfigFile == "" || sub.ConfigTmpl == "" {
		return "", "", nil
	}
	baseURL, rootURL := SubscriptionGatewayURLs(proxyURL, sub)
	if baseURL == "" {
		return "", "", nil
	}
	cfgTarget := resolveAgentConfigTarget(homeDir, env, sub.ConfigEnv, sub.ConfigFile)
	rendered := strings.ReplaceAll(sub.ConfigTmpl, "{base_url}", baseURL)
	rendered = strings.ReplaceAll(rendered, "{root_url}", rootURL)
	configPath, err = writePrivateGuestFile(cfgTarget, []byte(rendered), "subscription config")
	if err != nil {
		return "", "", err
	}
	if sub.AuthFormat == egress.AuthFormatCodexChatGPT && sub.AuthFile != "" {
		authTarget := resolveAgentConfigTarget(homeDir, env, sub.ConfigEnv, sub.AuthFile)
		authPath, err = writePrivateGuestFile(authTarget, CodexPlaceholderAuthJSON(time.Now().UTC()), "placeholder auth")
		if err != nil {
			return configPath, "", err
		}
	}
	return configPath, authPath, nil
}

func resolveAgentConfigTarget(homeDir string, env map[string]string, configEnv, relFile string) string {
	target := filepath.Join(homeDir, relFile)
	if configEnv != "" {
		if override := strings.TrimSpace(env[configEnv]); override != "" {
			target = filepath.Join(override, filepath.Base(relFile))
		}
	}
	return target
}

func writePrivateGuestFile(target string, data []byte, label string) (string, error) {
	dir := filepath.Clean(filepath.Dir(target))
	base := filepath.Base(target)
	if base == "." || base == ".." || strings.ContainsRune(base, filepath.Separator) {
		return "", fmt.Errorf("guest gateway: invalid %s file name %q. Try: check agent gateway config", label, target)
	}
	if err := paths.EnsurePrivateDir(dir); err != nil {
		return "", fmt.Errorf("guest gateway: create folder for %s: %w. Try: check sandbox home permissions", target, err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", fmt.Errorf("guest gateway: open folder %s: %w. Try: check sandbox home permissions", dir, err)
	}
	defer func() { _ = root.Close() }()
	tmpName := base + ".tmp"
	if err := root.WriteFile(tmpName, data, 0o600); err != nil {
		return "", fmt.Errorf("guest gateway: write %s: %w. Try: check sandbox home permissions", target, err)
	}
	if err := root.Rename(tmpName, base); err != nil {
		_ = root.Remove(tmpName)
		return "", fmt.Errorf("guest gateway: install %s: %w. Try: check sandbox home permissions", target, err)
	}
	return filepath.Join(dir, base), nil
}
