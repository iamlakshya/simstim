package supervisor

import (
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
)

func TestSupervisorBuildBoundCredentialsDefenseInDepth(t *testing.T) {
	newSup := func(agentKind string, binds ...api.SecretBind) *Supervisor {
		return &Supervisor{
			id: "test-def",
			m: &host.Manifest{
				ID:    "test-def",
				Agent: host.AgentInfo{Kind: agentKind},
				Wall:  &host.WallRecord{Driver: "process", BoundSecrets: binds},
			},
		}
	}

	// 1. Custom secret trying to hijack built-in "anthropic" route on a claude
	// session must NOT overwrite the route's upstream and must fail to arm.
	sup := newSup("claude",
		api.SecretBind{Name: "ACME_TOKEN", Hosts: []string{"api.acme.example"}, Header: "Authorization", Format: "Bearer {value}", Route: "anthropic"},
	)
	if got := sup.gatewayRouteHostMap()["anthropic"]; got != "api.anthropic.com" {
		t.Fatalf("anthropic route upstream = %q, want api.anthropic.com", got)
	}
	_, _, _, err := sup.buildBoundCredentials(sup.boundSecretMap(), []string{"ACME_TOKEN"}, map[string]api.Secret{"ACME_TOKEN": "top-secret"})
	if err == nil || !strings.Contains(err.Error(), "not its bound host") {
		t.Fatalf("hijacking built-in anthropic route: err = %v, want host mismatch refusal", err)
	}

	// 2. Two custom secrets colliding on explicit custom route.
	supDup := newSup("shell",
		api.SecretBind{Name: "ACME_A", Hosts: []string{"a.acme.example"}, Header: "Authorization", Format: "Bearer {value}", Route: "shared"},
		api.SecretBind{Name: "ACME_B", Hosts: []string{"b.acme.example"}, Header: "Authorization", Format: "Bearer {value}", Route: "shared"},
	)
	_, _, _, err = supDup.buildBoundCredentials(supDup.boundSecretMap(), []string{"ACME_A", "ACME_B"}, map[string]api.Secret{
		"ACME_A": "val-a",
		"ACME_B": "val-b",
	})
	if err == nil || !strings.Contains(err.Error(), "not its bound host") {
		t.Fatalf("duplicate custom route: err = %v, want host mismatch refusal", err)
	}

	// 3. Derived-slug collision (a-b.example vs a.b.example) refuses second secret.
	supDerived := newSup("shell",
		api.SecretBind{Name: "KEY_A", Hosts: []string{"a-b.example"}, Header: "Authorization", Format: "Bearer {value}"},
		api.SecretBind{Name: "KEY_B", Hosts: []string{"a.b.example"}, Header: "Authorization", Format: "Bearer {value}"},
	)
	_, _, _, err = supDerived.buildBoundCredentials(supDerived.boundSecretMap(), []string{"KEY_A", "KEY_B"}, map[string]api.Secret{
		"KEY_A": "val-a",
		"KEY_B": "val-b",
	})
	if err == nil || !strings.Contains(err.Error(), "not its bound host") {
		t.Fatalf("derived-slug collision: err = %v, want host mismatch refusal", err)
	}

	// 4. Two different secrets claiming the exact same host/route refuse to arm.
	supSame := newSup("claude",
		api.SecretBind{Name: "ANTHROPIC_API_KEY", Hosts: []string{"api.anthropic.com"}, Header: "x-api-key", Format: "{value}", Route: "anthropic"},
		api.SecretBind{Name: "CLAUDE_CODE_OAUTH_TOKEN", Hosts: []string{"api.anthropic.com"}, Header: "Authorization", Format: "Bearer {value}", Route: "anthropic"},
	)
	_, _, _, err = supSame.buildBoundCredentials(supSame.boundSecretMap(), []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"}, map[string]api.Secret{
		"ANTHROPIC_API_KEY":       "val-1",
		"CLAUDE_CODE_OAUTH_TOKEN": "val-2",
	})
	if err == nil || !strings.Contains(err.Error(), "both claim route") {
		t.Fatalf("duplicate secrets on same route: err = %v, want both claim route refusal", err)
	}

	// 5. Distinct valid routes arm cleanly and leave exposed secrets untouched.
	supOK := newSup("claude",
		api.SecretBind{Name: "ANTHROPIC_API_KEY", Hosts: []string{"api.anthropic.com"}, Header: "x-api-key", Format: "{value}", Route: "anthropic"},
		api.SecretBind{Name: "ACME_TOKEN", Hosts: []string{"api.acme.example"}, Header: "X-Acme", Format: "{value}", Route: "acme"},
	)
	creds, armed, exposed, err := supOK.buildBoundCredentials(supOK.boundSecretMap(), []string{"ANTHROPIC_API_KEY", "ACME_TOKEN", "PLAIN_EXPOSED"}, map[string]api.Secret{
		"ANTHROPIC_API_KEY": "ant-val",
		"ACME_TOKEN":        "acme-val",
		"PLAIN_EXPOSED":     "exp-val",
	})
	if err != nil {
		t.Fatalf("valid distinct routes failed: %v", err)
	}
	if len(creds) != 2 || len(armed) != 2 || len(exposed) != 1 || exposed[0] != "PLAIN_EXPOSED=exp-val" {
		t.Fatalf("unexpected valid result: creds=%d armed=%v exposed=%v", len(creds), armed, exposed)
	}
}
