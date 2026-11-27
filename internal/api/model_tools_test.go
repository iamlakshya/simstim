package api

import (
	"reflect"
	"testing"
)

func TestModelToolSetPreservesLegacyIdentityAndOwnsCanonicalData(t *testing.T) {
	rule := Rule{Layer: LayerSession, Allow: true, Door: DoorNet, Target: "api.openai.com"}
	legacy := RuleID(rule)
	rule.Tools = []string{}
	if RuleID(rule) != legacy {
		t.Fatal("empty capability set changed a legacy ID")
	}
	input := []string{"mcp", "files", "mcp"}
	tools, err := CanonicalModelTools(input)
	if err != nil || !reflect.DeepEqual(tools, []string{"files", "mcp"}) {
		t.Fatalf("canonical tools: %v, %v", tools, err)
	}
	input[0] = "web_fetch"
	rule.Tools = tools
	first := RuleID(rule)
	rule.Tools = []string{"mcp", "files"}
	if first == legacy || RuleID(rule) != first {
		t.Fatal("tools did not enter identity as one exact canonical set")
	}
	rule.Tools = []string{"mcp"}
	if RuleID(rule) == first {
		t.Fatal("different capabilities shared an identity")
	}
	copy := ClonePolicy(Policy{Rules: []Rule{rule}})
	copy.Rules[0].Tools[0] = "files"
	if rule.Tools[0] != "mcp" {
		t.Fatal("policy clone shares capability authority")
	}
	for _, invalid := range []string{"", "MCP", "file_search", "mcp\nfiles", "mcp\x00files"} {
		if _, err := CanonicalModelTools([]string{invalid}); err == nil {
			t.Fatalf("accepted capability %q", invalid)
		}
	}
}
