package host

import (
	"context"
	"slices"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestNamedSSHKeyPolicyChangesOnlyNextStartNames(t *testing.T) {
	layers := []api.Layer{
		{Kind: api.LayerAll, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{SSHKeys: []string{"DEPLOY_KEY"}}}},
		{Kind: api.LayerSession, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{SSHKeys: []string{"!DEPLOY_KEY", "OTHER_KEY"}}}},
	}
	policy, changed, err := ApplyNamedSSHKeyPolicy(layers, "DEPLOY_KEY", true)
	if err != nil || !slices.Contains(policy.Sandbox.SSHKeys, "DEPLOY_KEY") || !slices.Contains(policy.Sandbox.SSHKeys, "OTHER_KEY") {
		t.Fatal("next-start selection lost inherited or session names", err)
	}
	if !slices.Equal(layers[1].Sandbox.SSHKeys, []string{"!DEPLOY_KEY", "OTHER_KEY"}) {
		t.Fatal("policy edit changed its source layers")
	}
	policy, _, err = ApplyNamedSSHKeyPolicy(changed, "OTHER_KEY", false)
	if err != nil || slices.Contains(policy.Sandbox.SSHKeys, "OTHER_KEY") || !slices.Contains(policy.Sandbox.SSHKeys, "DEPLOY_KEY") {
		t.Fatal("next-start removal changed another selection", err)
	}
	for _, name := range []string{"", "SSH_AUTH_SOCK", "!DEPLOY_KEY", "DEPLOY KEY", "DEPLOY\nKEY"} {
		if _, _, err := ApplyNamedSSHKeyPolicy(layers, name, true); err == nil {
			t.Fatal("invalid key selection accepted")
		}
	}
}

func TestNamedSSHColdSelectionCannotBeAValueFreePlaceholder(t *testing.T) {
	policy := api.Policy{LayerValues: api.LayerValues{Sandbox: api.SandboxValues{SSHKeys: []string{"DEPLOY_KEY"}}}}
	if checkNewNamedSSHSelection(policy, nil) == nil {
		t.Fatal("selected key without a cold value delivery was accepted")
	}
	keys := &api.NamedSSHKeysV1{Version: 1, Names: []string{"DEPLOY_KEY"}, Values: map[string]api.Secret{"DEPLOY_KEY": "private-fixture"}}
	if err := checkNewNamedSSHSelection(policy, keys); err != nil {
		t.Fatal(err)
	}
	if captureNewNamedSSHKeys(context.Background(), nil, nil, NewArgs{NamedSSHKeys: keys}) == nil {
		t.Fatal("data-only keys created an original cold-start reservation")
	}
	if _, err := newUnwalled(context.Background(), NewArgs{NamedSSHKeys: keys}); err == nil {
		t.Fatal("unwalled start silently ignored a named-key delivery")
	}
}
