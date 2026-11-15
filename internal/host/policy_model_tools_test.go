package host

import (
	"errors"
	"reflect"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestModelToolRegrantKeepsExactSetAndFreshLeaseInstance(t *testing.T) {
	args := api.PolicyAddArgs{Allow: true, Target: "api.openai.com", Tools: []string{"mcp", "files", "mcp"}, Once: true}
	_, firstLayers, first, err := ApplyPolicyAdd(nil, args)
	if err != nil || !reflect.DeepEqual(first.Tools, []string{"files", "mcp"}) || first.GrantID == "" {
		t.Fatalf("initial capability grant: %+v, %v", first, err)
	}
	args.Tools = []string{"files", "mcp"}
	_, secondLayers, second, err := ApplyPolicyAdd(firstLayers, args)
	if err != nil || len(secondLayers) != 1 || len(secondLayers[0].Rules) != 2 || second.ID != first.ID || second.GrantID == first.GrantID {
		t.Fatalf("exact-set once grant did not append a fresh lease: %+v, %v", second, err)
	}
	if firstLayers[0].Rules[0].GrantID != first.GrantID {
		t.Fatal("regrant mutated the prior trusted layer")
	}
	args.Tools = []string{"mcp"}
	policy, thirdLayers, third, err := ApplyPolicyAdd(secondLayers, args)
	if err != nil || third.ID == second.ID || len(policy.Rules) != 3 {
		t.Fatalf("a different exact capability set replaced prior authority: %+v, %v", policy.Rules, err)
	}
	remaining, _, removed, err := ApplyPolicyRm(thirdLayers, api.PolicyRmArgs{ID: second.ID})
	if err != nil || removed != 2 || len(remaining.Rules) != 1 || !reflect.DeepEqual(remaining.Rules[0].Tools, []string{"mcp"}) {
		t.Fatalf("ID removal crossed a capability-set boundary: %+v, removed=%d, %v", remaining.Rules, removed, err)
	}
}

func TestModelToolGrantRejectsScopeAndHostBeforePublishingLayers(t *testing.T) {
	for _, args := range []api.PolicyAddArgs{
		{Allow: false, Target: "api.openai.com", Tools: []string{"mcp"}},
		{Allow: true, Target: "*.openai.com", Tools: []string{"mcp"}},
		{Allow: true, Target: "api.openai.com/v1/responses", Tools: []string{"mcp"}},
		{Allow: true, Target: "api.openai.com:444", Tools: []string{"mcp"}},
		{Allow: true, Target: "example.com", Tools: []string{"mcp"}},
		{Allow: true, Target: "api.openai.com", Tools: []string{"file_search"}},
	} {
		policy, layers, _, err := ApplyPolicyAdd(nil, args)
		var detail *proto.Error
		if !errors.As(err, &detail) || detail.Code != "bad_tools" || detail.Hint == "" || layers != nil || len(policy.Rules) != 0 {
			t.Fatalf("invalid capability grant published state or lost hint: %+v, %v", args, err)
		}
	}
}
