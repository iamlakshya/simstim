package egress

import (
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func modelTestEngine(t *testing.T, agent string, preset api.Preset, tools ...string) *Engine {
	t.Helper()
	host := "api.openai.com"
	if agent == "claude" {
		host = "api.anthropic.com"
	}
	policy := api.Policy{Preset: preset}
	if len(tools) != 0 {
		policy.Rules = []api.Rule{{Layer: api.LayerSession, Allow: true, Door: api.DoorNet, Target: host, Tools: tools}}
	}
	engine, err := NewEngine(agent, policy)
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func TestModelRequestTypesAndNestedProviderFetches(t *testing.T) {
	for _, test := range []struct {
		name, agent, body string
		preset            api.Preset
		grants            []string
		refusal           string
	}{
		{"function union schema", "codex", `{"tools":[{"type":"function","parameters":{"type":["object","null"]}}]}`, api.PresetLocked, nil, ""},
		{"client tool", "claude", `{"tools":[{"name":"local"}],"messages":[]}`, api.PresetLocked, nil, ""},
		{"unknown dated tool", "claude", `{"tools":[{"type":"new_server_tool_20990101"}]}`, api.PresetOpen, nil, "parse"},
		{"web fetch", "claude", `{"tools":[{"type":"web_fetch_20260318"}]}`, api.PresetOpen, nil, "web_fetch"},
		{"granted web fetch", "claude", `{"tools":[{"type":"web_fetch_20260318"}]}`, api.PresetLocked, []string{"web_fetch"}, ""},
		{"MCP server", "claude", `{"mcp_servers":[{"type":"url","url":"https://example.com"}]}`, api.PresetOpen, nil, "mcp"},
		{"MCP toolset", "claude", `{"tools":[{"type":"mcp_toolset"}]}`, api.PresetBalanced, nil, "mcp"},
		{"document inside tool result", "claude", `{"messages":[{"content":[{"type":"tool_result","content":[{"source":{"type":"url","url":"https://example.com"},"type":"document"}]}]}]}`, api.PresetOpen, nil, "url_content"},
		{"data image", "codex", `{"input":[{"type":"input_image","image_url":"data:image/png;base64,AA=="}]}`, api.PresetLocked, nil, ""},
		{"file ID", "codex", `{"input":[{"type":"input_file","file_id":"file_1"}]}`, api.PresetLocked, nil, ""},
		{"remote input file", "codex", `{"input":[{"type":"message","content":[{"file_url":"HTTPS://example.com/file","type":"input_file"}]}]}`, api.PresetBalanced, nil, "url_content"},
		{"OpenAI MCP", "codex", `{"tools":[{"type":"mcp","connector_id":"connector"}]}`, api.PresetOpen, nil, "mcp"},
		{"interpreter grant", "codex", `{"tools":[{"type":"code_interpreter"}]}`, api.PresetLocked, []string{"code_execution"}, ""},
		{"file search never baseline", "codex", `{"tools":[{"type":"file_search"}]}`, api.PresetOpen, []string{"files"}, "parse"},
		{"locked search", "codex", `{"tools":[{"type":"web_search"}]}`, api.PresetLocked, nil, "web_search"},
		{"balanced search", "codex", `{"tools":[{"type":"web_search"}]}`, api.PresetBalanced, nil, ""},
		{"granted locked search", "codex", `{"tools":[{"type":"web_search"}]}`, api.PresetLocked, []string{"web_search"}, ""},
		{"unlisted dated search", "codex", `{"tools":[{"type":"web_search_2099_01_01"}]}`, api.PresetBalanced, nil, "parse"},
		{"duplicate type", "codex", `{"tools":[{"type":"mcp","type":"function"}]}`, api.PresetOpen, nil, "parse"},
		{"trailing document", "codex", `{"input":[]} {}`, api.PresetOpen, nil, "parse"},
	} {
		t.Run(test.name, func(t *testing.T) {
			engine := modelTestEngine(t, test.agent, test.preset, test.grants...)
			host, path := "api.openai.com", "/v1/responses"
			if test.agent == "claude" {
				host, path = "api.anthropic.com", "/v1/messages"
			}
			request := Request{Host: host, Port: 443, Method: "POST", Path: path}
			model := &modelRequest{request: request, guard: engine.modelGuard(request)}
			parseErr := model.inspect([]byte(test.body), false)
			if test.refusal == "parse" {
				if parseErr == nil {
					t.Fatal("unlisted or ambiguous request passed")
				}
				return
			}
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			if refusal := model.refusal(engine, nil, evalPrecheck); refusal != test.refusal {
				t.Fatalf("refusal %q, want %q", refusal, test.refusal)
			}
		})
	}
}

func TestModelToolsRemainExactSessionInstancesAndNeverClaimUnneededOnce(t *testing.T) {
	base := api.Rule{Layer: api.LayerSession, Allow: true, Door: api.DoorNet, Target: "api.openai.com", Tools: []string{"mcp"}, Once: true, GrantID: "model-lease"}
	other := base
	other.Tools = []string{"files"}
	if same, err := MatchesRuleReplacement(base, other); err != nil || same {
		t.Fatalf("different tool sets replace each other: %v, %v", same, err)
	}
	if same, err := EqualRuleInstances(base, other); err != nil || same {
		t.Fatalf("different tool sets compare equal: %v, %v", same, err)
	}
	for _, mutate := range []func(*api.Rule){
		func(r *api.Rule) { r.Layer = api.LayerAll }, func(r *api.Rule) { r.Allow = false },
		func(r *api.Rule) { r.Target = "*.openai.com" }, func(r *api.Rule) { r.Target = "api.openai.com/v1/responses" },
		func(r *api.Rule) { r.Target = "api.openai.com:444" }, func(r *api.Rule) { r.Target = "example.com" },
		func(r *api.Rule) { r.Source = api.SourceRepositoryFile },
	} {
		invalid := base
		mutate(&invalid)
		if _, err := CanonicalRuleSelection(invalid); err == nil {
			t.Fatal("accepted a capability outside the exact session host")
		}
	}
	engine, err := NewEngine("codex", api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{base, other}})
	if err != nil {
		t.Fatal(err)
	}
	request := Request{Host: "api.openai.com", Port: 443, Method: "POST", Path: "/v1/responses"}
	permit := engine.NewPermit()
	if !engine.evaluateModelNetwork(request, permit, evalReserve).Allow || len(permit.owned) != 0 {
		t.Fatal("network permission consumed an unneeded tool lease")
	}
	permit.Commit() // The SAME connected WS permit acquires its first message's capability.
	model := &modelRequest{request: request, guard: engine.modelGuard(request), features: []string{"mcp", "url_content"}}
	if got := model.refusal(engine, permit, evalReserve); got != "url_content" || len(permit.owned) != 0 {
		t.Fatal("partial model refusal reserved a lease")
	}
	model.features = []string{"mcp"}
	if got := model.refusal(engine, permit, evalReserve); got != "" || len(permit.owned) != 1 {
		t.Fatalf("connected model capability acquisition: %q", got)
	}
	concurrent := engine.NewPermit()
	if got := model.refusal(engine, concurrent, evalReserve); got != "mcp" || len(concurrent.owned) != 0 {
		t.Fatal("one-use capability was shared")
	}
	if got := model.refusal(engine, permit, evalRevalidate); got != "" {
		t.Fatal("original request lost its owned capability")
	}
	ended := permit.Close()
	if len(ended) != 1 || ended[0].GrantID != "model-lease" {
		t.Fatal("tool lease did not end through the original permit")
	}
	if !engine.modelToolAllowed(request, "files", nil, evalPrecheck) {
		t.Fatal("unneeded files-once was spent")
	}
	// Reused short IDs cannot make a changed capability reuse a prior slot.
	left, _ := compileRule(base)
	right, _ := compileRule(other)
	right.id = left.id
	if left.instanceKey() == right.instanceKey() {
		t.Fatal("sequential changed tool sets share a lease key")
	}
	until := time.Now().Add(-time.Second)
	expired := other
	expired.Until = until
	expiredEngine, err := NewEngine("codex", api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{expired}})
	if err != nil || expiredEngine.modelToolAllowed(request, "files", nil, evalPrecheck) {
		t.Fatal("expired capability retained authority")
	}
	borrowed := other
	borrowed.ID = api.RuleID(base)
	if _, err := CanonicalRuleInstances([]api.Rule{borrowed}); err == nil || !strings.Contains(err.Error(), "noncanonical") {
		t.Fatal("capability accepted a borrowed public ID")
	}
}
