package agents

import "slices"

// ModelGuard describes a model protocol and the exact request and tool types
// that its agent uses. Source names the evidence for this baseline; recordings
// remain a separate release check and are never manufactured by unit tests.
type ModelGuard struct {
	Wire  string   `json:"wire"`
	Hosts []string `json:"hosts"`
	Paths []string `json:"paths"`
	// Files lists exact method/authority/path upload endpoints.
	Files     []string `json:"files,omitempty"`
	Tools     []string `json:"tools"`
	Search    []string `json:"search,omitempty"`
	WebSocket bool     `json:"websocket,omitempty"`
	Source    string   `json:"source"`
}

// These documented baselines come from isolation spec 7.2/8.6 and build facts
// 1.2/2.4/2.5. They are not a claim about captured agent traffic. A new dated
// server tool needs a reviewed row change rather than a prefix allowance.
var modelData = map[string]ModelGuard{
	"claude": {
		Wire: "anthropic-messages", Hosts: []string{"api.anthropic.com"},
		Files:  []string{"POST api.anthropic.com/v1/files"},
		Paths:  []string{"POST /v1/messages", "POST /v1/messages/count_tokens"},
		Tools:  []string{"custom", "computer_20250124", "text_editor_20250124", "bash_20250124"},
		Search: []string{"web_search_20250305", "web_search_20260209", "web_search_20260318"},
		Source: "https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-reference",
	},
	"codex": {
		Wire: "openai-responses", Hosts: []string{"api.openai.com", "chatgpt.com"},
		Files:  []string{"POST api.openai.com/v1/files"},
		Paths:  []string{"POST /v1/responses", "POST /v1/chat/completions", "GET /v1/responses", "GET /v1/models", "POST /backend-api/codex/responses", "GET /backend-api/codex/responses", "GET /backend-api/codex/models"},
		Tools:  []string{"function", "custom", "computer", "local_shell"},
		Search: []string{"web_search", "web_search_2025_08_26"}, WebSocket: true,
		Source: "https://github.com/openai/codex/blob/main/codex-rs/model-provider-info/src/lib.rs",
	},
}

func withModelData(table []Agent) []Agent {
	for i := range table {
		if data, ok := modelData[table[i].Kind]; ok {
			table[i].ModelGuard = cloneModelGuard(&data)
		}
	}
	return table
}

// ModelPolicy returns an owned model guard. Callers cannot mutate agent data.
func (agent Agent) ModelPolicy() *ModelGuard { return cloneModelGuard(agent.ModelGuard) }

func cloneModelGuard(input *ModelGuard) *ModelGuard {
	if input == nil {
		return nil
	}
	output := *input
	output.Hosts, output.Paths = slices.Clone(input.Hosts), slices.Clone(input.Paths)
	output.Tools, output.Search = slices.Clone(input.Tools), slices.Clone(input.Search)
	output.Files = slices.Clone(input.Files)
	return &output
}

// ModelHostPolicy returns the documented protocol baseline for a known model
// host when a shell or custom command has no selected-agent row. It supplies
// no network allowance; the session policy must still allow the destination.
func ModelHostPolicy(host string) *ModelGuard {
	for _, agent := range Table {
		if guard := agent.ModelGuard; guard != nil && slices.Contains(guard.Hosts, host) {
			return cloneModelGuard(guard)
		}
	}
	return nil
}
