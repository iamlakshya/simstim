package agents

import (
	"net/url"
	"slices"
	"strings"
)

// SecretBinding describes how one credential environment variable maps to
// outbound HTTP headers on its bound hosts (spec 8.6).
type SecretBinding struct {
	Env    string   `json:"env"`              // e.g. "ANTHROPIC_API_KEY"
	Hosts  []string `json:"hosts"`            // e.g. ["api.anthropic.com"]
	Header string   `json:"header"`           // e.g. "x-api-key" or "Authorization"
	Format string   `json:"format"`           // e.g. "{value}" or "Bearer {value}"
	Source string   `json:"source,omitempty"` // link to the vendor's docs
}

// FormatHeader renders the outbound header name and value for a raw secret
// value. It defaults to "{value}" when Format is empty.
func (b SecretBinding) FormatHeader(value string) (string, string) {
	format := b.Format
	if format == "" {
		format = "{value}"
	}
	return b.Header, strings.ReplaceAll(format, "{value}", value)
}

// ModelGateway describes how a sandboxed agent reaches its model provider
// through the egress process's plain-HTTP gateway route instead of direct TLS
// CONNECT (decision 0053, spec 7.2 and 7.3). All settings are data so no code
// branches on an agent's name.
type ModelGateway struct {
	// Route is the path segment under /gw/<route> served by the egress process.
	Route string `json:"route"`
	// Upstream is the verified HTTPS origin of the provider, e.g. "https://api.anthropic.com".
	Upstream string `json:"upstream"`
	// BaseURLEnv is the environment variable that points the agent at the
	// gateway route, e.g. "ANTHROPIC_BASE_URL".
	BaseURLEnv string `json:"base_url_env,omitempty"`
	// BaseURLPath is an optional path suffix appended to /gw/<route> for the
	// agent's base URL, e.g. "/v1" for OpenAI/Codex clients.
	BaseURLPath string `json:"base_url_path,omitempty"`
	// DummyEnv is the default credential environment variable set to a
	// non-secret placeholder inside the sandbox when no specific secret name
	// is bound.
	DummyEnv string `json:"dummy_env,omitempty"`
	// ConfigFile is an optional relative path under the guest's home (or
	// ConfigEnv) where the guest writes a provider block, e.g. ".codex/config.toml".
	ConfigFile string `json:"config_file,omitempty"`
	// ConfigEnv is an optional environment variable whose directory overrides
	// the parent folder of ConfigFile, e.g. "CODEX_HOME".
	ConfigEnv string `json:"config_env,omitempty"`
	// ConfigTmpl is the template written to ConfigFile. {base_url} and
	// {env_key} are replaced.
	ConfigTmpl string `json:"config_tmpl,omitempty"`
	// Source cites the vendor documentation for the gateway configuration.
	Source string `json:"source,omitempty"`
}

// UpstreamHost returns the lower-case hostname of g.Upstream, or "".
func (g *ModelGateway) UpstreamHost() string {
	if g == nil || g.Upstream == "" {
		return ""
	}
	u, err := url.Parse(g.Upstream)
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
}

// SubscriptionGateway describes an OAuth subscription model gateway route
// (such as Codex's ChatGPT sign-in) where the trusted egress process injects
// and refreshes OAuth tokens on Upstream while the sandbox receives only a
// placeholder config and placeholder auth file (decision 0059, spec 7.3).
type SubscriptionGateway struct {
	Route         string      `json:"route"`
	Upstream      string      `json:"upstream"`
	BaseURLPath   string      `json:"base_url_path,omitempty"`
	RootURLPath   string      `json:"root_url_path,omitempty"`
	SecretEnv     string      `json:"secret_env,omitempty"`
	AuthFile      string      `json:"auth_file,omitempty"`
	AuthFormat    string      `json:"auth_format"`
	TokenURL      string      `json:"token_url"`
	ClientID      string      `json:"client_id"`
	AccountHeader string      `json:"account_header,omitempty"`
	ConfigFile    string      `json:"config_file,omitempty"`
	ConfigEnv     string      `json:"config_env,omitempty"`
	ConfigTmpl    string      `json:"config_tmpl,omitempty"`
	Guard         *ModelGuard `json:"guard,omitempty"`
	Source        string      `json:"source,omitempty"`
}

// UpstreamHost returns the lower-case hostname of g.Upstream, or "".
func (g *SubscriptionGateway) UpstreamHost() string {
	if g == nil || g.Upstream == "" {
		return ""
	}
	u, err := url.Parse(g.Upstream)
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
}

// ModelGuard describes what the egress gateway permits on an agent's model API
// route (spec 7.2 and 8.6). Anything not listed fails closed.
type ModelGuard struct {
	// Wire names the API wire format: "anthropic-messages", "openai-responses"
	// or "openai-chat".
	Wire string `json:"wire"`
	// Hosts lists the model API hostnames that must never be reached by direct
	// CONNECT so a key planted inside the sandbox cannot bypass the gateway.
	Hosts []string `json:"hosts"`
	// Paths lists allowed "METHOD /path" endpoints, e.g. "POST /v1/messages".
	Paths []string `json:"paths"`
	// Files lists file-upload "METHOD /path" endpoints refused unless granted
	// with --tools files.
	Files []string `json:"files,omitempty"`
	// Tools lists allowed tools[].type values (entries with no type always pass).
	Tools []string `json:"tools"`
	// Search lists web-search tools[].type values allowed under balanced and
	// open presets, and refused under locked unless granted with --tools web_search.
	Search []string `json:"search,omitempty"`
}

// ModelHosts returns the sorted, deduplicated list of model API hostnames
// declared across the built-in agent table. Direct CONNECT to these hosts is
// always refused so a key planted inside a sandbox cannot bypass the gateway.
func ModelHosts() []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(h string) {
		h = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(h), "."))
		if h == "" {
			return
		}
		if _, ok := seen[h]; !ok {
			seen[h] = struct{}{}
			out = append(out, h)
		}
	}
	for _, a := range Table {
		if a.ModelGuard != nil {
			for _, h := range a.ModelGuard.Hosts {
				add(h)
			}
		}
		if a.ModelGateway != nil {
			add(a.ModelGateway.UpstreamHost())
		}
		if a.SubscriptionGateway != nil {
			add(a.SubscriptionGateway.UpstreamHost())
			if a.SubscriptionGateway.Guard != nil {
				for _, h := range a.SubscriptionGateway.Guard.Hosts {
					add(h)
				}
			}
		}
	}
	slices.Sort(out)
	return out
}

// FindSecretBinding looks up the SecretBinding on agent a matching envName.
// If envName is empty and a declares at least one binding, the first binding
// is returned.
func FindSecretBinding(a Agent, envName string) (SecretBinding, bool) {
	if len(a.Secrets) == 0 {
		return SecretBinding{}, false
	}
	if envName == "" {
		return a.Secrets[0], true
	}
	for _, b := range a.Secrets {
		if b.Env == envName {
			return b, true
		}
	}
	return SecretBinding{}, false
}

// FindSecretBindingAnywhere searches the built-in agent table for a
// SecretBinding matching envName (for example when a user runs a shell sandbox
// with --secret ANTHROPIC_API_KEY).
func FindSecretBindingAnywhere(envName string) (Agent, SecretBinding, bool) {
	if envName == "" {
		return Agent{}, SecretBinding{}, false
	}
	for _, a := range Table {
		for _, b := range a.Secrets {
			if b.Env == envName {
				return a, b, true
			}
		}
	}
	return Agent{}, SecretBinding{}, false
}

// FindGatewayForHost looks up the built-in ModelGateway whose UpstreamHost
// matches rawHost.
func FindGatewayForHost(rawHost string) (Agent, *ModelGateway, bool) {
	want := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(rawHost), "."))
	if want == "" {
		return Agent{}, nil, false
	}
	for _, a := range Table {
		if a.ModelGateway != nil && a.ModelGateway.UpstreamHost() == want {
			return a, a.ModelGateway, true
		}
	}
	return Agent{}, nil, false
}

// FindGatewayRouteForHost returns the built-in gateway route slug for rawHost
// across both ModelGateway and SubscriptionGateway entries in Table.
func FindGatewayRouteForHost(rawHost string) (string, bool) {
	want := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(rawHost), "."))
	if want == "" {
		return "", false
	}
	for _, a := range Table {
		if a.ModelGateway != nil && a.ModelGateway.UpstreamHost() == want {
			return a.ModelGateway.Route, true
		}
		if a.SubscriptionGateway != nil && a.SubscriptionGateway.UpstreamHost() == want {
			return a.SubscriptionGateway.Route, true
		}
	}
	return "", false
}

// FindSubscriptionGatewayBySecret looks up an agent's SubscriptionGateway
// matching secretEnv (for example "CODEX_AUTH_JSON").
func FindSubscriptionGatewayBySecret(secretEnv string) (Agent, *SubscriptionGateway, bool) {
	if secretEnv == "" {
		return Agent{}, nil, false
	}
	for _, a := range Table {
		if a.SubscriptionGateway != nil && a.SubscriptionGateway.SecretEnv == secretEnv {
			return a, a.SubscriptionGateway, true
		}
	}
	return Agent{}, nil, false
}

var claudeSecrets = []SecretBinding{
	{
		Env:    "ANTHROPIC_API_KEY",
		Hosts:  []string{"api.anthropic.com"},
		Header: "x-api-key",
		Format: "{value}",
		Source: "https://code.claude.com/docs/en/authentication",
	},
	{
		Env:    "ANTHROPIC_AUTH_TOKEN",
		Hosts:  []string{"api.anthropic.com"},
		Header: "Authorization",
		Format: "Bearer {value}",
		Source: "https://code.claude.com/docs/en/authentication",
	},
	{
		// Verified against real claude 2.1.332 on host-a: sends POST /v1/messages?beta=true
		// with Authorization: Bearer <CLAUDE_CODE_OAUTH_TOKEN>, anthropic-beta containing
		// oauth-2025-04-20, and no x-api-key header.
		Env:    "CLAUDE_CODE_OAUTH_TOKEN",
		Hosts:  []string{"api.anthropic.com"},
		Header: "Authorization",
		Format: "Bearer {value}",
		Source: "https://code.claude.com/docs/en/authentication",
	},
}

var claudeGateway = &ModelGateway{
	Route:      "anthropic",
	Upstream:   "https://api.anthropic.com",
	BaseURLEnv: "ANTHROPIC_BASE_URL",
	DummyEnv:   "ANTHROPIC_API_KEY",
	Source:     "https://code.claude.com/docs/en/llm-gateway",
}

var claudeGuard = &ModelGuard{
	Wire:  "anthropic-messages",
	Hosts: []string{"api.anthropic.com"},
	Paths: []string{
		"POST /v1/messages",
		"POST /v1/messages/count_tokens",
	},
	Files: []string{"POST /v1/files"},
	Tools: []string{
		"custom",
		"bash_20241022",
		"bash_20250124",
		"text_editor_20241022",
		"text_editor_20250124",
		"text_editor_20250429",
		"text_editor_20250728",
		"advisor_20260301",
		"tool_search_tool_regex_20251119",
		"tool_search_tool_bm25_20251119",
	},
	Search: []string{
		"web_search_20250305",
		"web_search_20260209",
		"web_search_20260318",
	},
}

var codexSecrets = []SecretBinding{
	{
		Env:    "OPENAI_API_KEY",
		Hosts:  []string{"api.openai.com"},
		Header: "Authorization",
		Format: "Bearer {value}",
		Source: "https://learn.chatgpt.com/docs/auth",
	},
	{
		Env:    "CODEX_API_KEY",
		Hosts:  []string{"api.openai.com"},
		Header: "Authorization",
		Format: "Bearer {value}",
		Source: "https://learn.chatgpt.com/docs/auth",
	},
	{
		// ChatGPT sign-in bundle (~/.codex/auth.json JSON) pushed from the
		// laptop when not using a host-side codex login --device-auth (decision 0059).
		Env:    "CODEX_AUTH_JSON",
		Hosts:  []string{"chatgpt.com"},
		Header: "Authorization",
		Format: "Bearer {value}",
		Source: "https://learn.chatgpt.com/docs/auth",
	},
}

const codexGatewayConfigTmpl = `model_provider = "gateway"

[model_providers.gateway]
name = "gateway"
base_url = "{base_url}"
env_key = "{env_key}"
wire_api = "responses"
supports_websockets = false
`

const codexSubscriptionConfigTmpl = `cli_auth_credentials_store = "file"
model_provider = "gateway"
chatgpt_base_url = "{root_url}"

[model_providers.gateway]
name = "gateway"
base_url = "{base_url}"
wire_api = "responses"
requires_openai_auth = true
supports_websockets = false
`

//nolint:gosec // G101: public OAuth client_id and endpoint URL from openai/codex, not a secret
var codexSubscriptionGateway = &SubscriptionGateway{
	Route:         "chatgpt",
	Upstream:      "https://chatgpt.com",
	BaseURLPath:   "/backend-api/codex",
	RootURLPath:   "/backend-api",
	SecretEnv:     "CODEX_AUTH_JSON",
	AuthFile:      ".codex/auth.json",
	AuthFormat:    "codex-chatgpt",
	TokenURL:      "https://auth.openai.com/oauth/token",
	ClientID:      "app_EMoamEEZ73f0CkXaXp7hrann",
	AccountHeader: "Chatgpt-Account-Id",
	ConfigFile:    ".codex/config.toml",
	ConfigEnv:     "CODEX_HOME",
	ConfigTmpl:    codexSubscriptionConfigTmpl,
	Guard:         codexChatGPTGuard,
	Source:        "https://learn.chatgpt.com/docs/auth",
}

var codexChatGPTGuard = &ModelGuard{
	Wire:  "openai-responses",
	Hosts: []string{"chatgpt.com"},
	Paths: []string{
		"POST /backend-api/codex/responses",
		"POST /backend-api/codex/responses/compact",
		"POST /backend-api/codex/responses/input_tokens",
		"GET /backend-api/codex/models",
	},
	Tools: []string{
		"function",
		"custom",
		"namespace",
		"local_shell",
		"apply_patch",
		"shell",
		"computer",
	},
	Search: []string{
		"web_search",
		"web_search_2025_08_26",
		"web_search_preview",
		"web_search_preview_2025_03_11",
	},
}

var codexGateway = &ModelGateway{
	Route:       "openai",
	Upstream:    "https://api.openai.com",
	BaseURLEnv:  "OPENAI_BASE_URL",
	BaseURLPath: "/v1",
	DummyEnv:    "OPENAI_API_KEY",
	ConfigFile:  ".codex/config.toml",
	ConfigEnv:   "CODEX_HOME",
	ConfigTmpl:  codexGatewayConfigTmpl,
	Source:      "https://coder.com/docs/ai-coder/ai-gateway/clients/codex",
}

var codexGuard = &ModelGuard{
	Wire:  "openai-responses",
	Hosts: []string{"api.openai.com"},
	Paths: []string{
		"POST /v1/responses",
		"POST /v1/responses/input_tokens",
		"POST /v1/responses/compact",
		"POST /v1/chat/completions",
		"GET /v1/models",
	},
	Files: []string{"POST /v1/files"},
	Tools: []string{
		"function",
		"custom",
		"namespace",
		"local_shell",
		"apply_patch",
		"shell",
		"computer",
	},
	Search: []string{
		"web_search",
		"web_search_2025_08_26",
		"web_search_preview",
		"web_search_preview_2025_03_11",
	},
}
