package host

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestHostPushAdditionRetainsExactModeAndIndependentLease(t *testing.T) {
	args := api.PolicyAddArgs{Door: api.DoorPush, Allow: true, Target: "github.com/org/repo", Once: true}
	_, layers, first, err := ApplyPolicyAdd(nil, args)
	if err != nil || first.Mode != "" || first.Target != args.Target || first.GrantID == "" {
		t.Fatalf("first push: %+v %v", first, err)
	}
	policy, _, second, err := ApplyPolicyAdd(layers, args)
	if err != nil || len(policy.Rules) != 2 || second.ID != first.ID || second.GrantID == first.GrantID {
		t.Fatal("push regrant erased an independent active lease")
	}
	args.Mode = api.PushModePublic
	_, _, public, err := ApplyPolicyAdd(nil, args)
	if err != nil || public.Mode != api.PushModePublic || public.ID == first.ID {
		t.Fatal("explicit public mode did not retain its own rule identity")
	}
}

func TestHostPushScopeRefusesMethodsToolsAndNonRepositoryTargets(t *testing.T) {
	methods := []string{"POST"}
	for _, args := range []api.PolicyAddArgs{
		{Door: api.DoorPush, Allow: true, Target: "github.com"},
		{Door: api.DoorPush, Allow: true, Target: "*.github.com/org/repo"},
		{Door: api.DoorPush, Allow: true, Target: "github.com/org/repo", Methods: &methods},
		{Door: api.DoorPush, Allow: true, Target: "github.com/org/repo", Tools: []string{"mcp"}},
		{Door: api.DoorPush, Allow: true, Target: "github.com/org/repo", Mode: "unexpected"},
	} {
		policy, layers, _, err := ApplyPolicyAdd(nil, args)
		if err == nil || layers != nil || len(policy.Rules) != 0 {
			t.Fatal("invalid push scope published policy")
		}
	}
}
