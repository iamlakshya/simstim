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
}

const codexGatewayConfigTmpl = `model_provider = "gateway"

[model_providers.gateway]
name = "gateway"
base_url = "{base_url}"
env_key = "{env_key}"
wire_api = "responses"
supports_websockets = false
`

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
