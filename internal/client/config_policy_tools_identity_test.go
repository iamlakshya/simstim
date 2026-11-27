package client

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestPolicyMetadataKeyRetainsCanonicalToolScopeAndLegacyEmpty(t *testing.T) {
	base := api.Rule{Layer: api.LayerSession, Door: api.DoorNet, Allow: true, Target: "api.openai.com"}
	empty := base
	empty.Tools = []string{}
	if completionRuleKey(base) != completionRuleKey(empty) {
		t.Fatal("emptytools changed legacy metadata identity")
	}
	mcp := base
	mcp.Tools = []string{"mcp"}
	files := base
	files.Tools = []string{"files"}
	if completionRuleKey(mcp) == completionRuleKey(files) {
		t.Fatal("different model capabilities collapsed")
	}
	left, right := base, base
	var err error
	left.Tools, err = api.CanonicalModelTools([]string{"mcp", "files"})
	if err != nil {
		t.Fatal(err)
	}
	right.Tools, err = api.CanonicalModelTools([]string{"files", "mcp"})
	if err != nil {
		t.Fatal(err)
	}
	if completionRuleKey(left) != completionRuleKey(right) {
		t.Fatal("canonical tool ordering changed metadata identity")
	}
}
