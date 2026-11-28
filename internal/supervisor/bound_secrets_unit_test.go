package supervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
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

func TestSupervisorCodexSubscriptionCredentialsAndStartReq(t *testing.T) {
	// 1. Laptop-pushed CODEX_AUTH_JSON parses into memory-only OAuthConfig (AuthFile == "").
	supSecret := &Supervisor{
		id: "test-sub-secret",
		m: &host.Manifest{
			ID:    "test-sub-secret",
			Agent: host.AgentInfo{Kind: "codex"},
			Wall: &host.WallRecord{
				Driver:           "process",
				SubscriptionAuth: "secret:CODEX_AUTH_JSON",
				BoundSecrets: []api.SecretBind{
					{Name: "CODEX_AUTH_JSON", Hosts: []string{"chatgpt.com"}, Header: "Authorization", Format: "Bearer {value}", Route: "chatgpt"},
					{Name: "ACME_TOKEN", Hosts: []string{"api.acme.example"}, Header: "X-Acme", Format: "{value}", Route: "acme"},
				},
			},
		},
	}
	rawAuth := `{"auth_mode":"chatgpt","tokens":{"access_token":"acc-tok","refresh_token":"ref-tok","id_token":"id-tok","account_id":"acct_laptop"}}`
	creds, armed, _, err := supSecret.buildBoundCredentials(supSecret.boundSecretMap(), []string{"CODEX_AUTH_JSON", "ACME_TOKEN"}, map[string]api.Secret{
		"CODEX_AUTH_JSON": api.Secret(rawAuth),
		"ACME_TOKEN":      "acme-val",
	})
	if err != nil {
		t.Fatalf("buildBoundCredentials(CODEX_AUTH_JSON): %v", err)
	}
	if len(creds) != 2 || len(armed) != 2 {
		t.Fatalf("creds=%d armed=%v", len(creds), armed)
	}
	subCred := creds[0]
	if subCred.Route != "chatgpt" || string(subCred.Value) != "acc-tok" || subCred.OAuth == nil ||
		subCred.OAuth.Format != egress.AuthFormatCodexChatGPT ||
		subCred.OAuth.AccountHeader != "Chatgpt-Account-Id" ||
		subCred.OAuth.AccountID != "acct_laptop" ||
		string(subCred.OAuth.RefreshToken) != "ref-tok" ||
		subCred.OAuth.AuthFile != "" {
		t.Fatalf("unexpected laptop subscription cred: %+v", subCred)
	}
	// buildStartReq sets SubscriptionRoute="chatgpt" and omits CODEX_AUTH_JSON from BoundPlaceholders.
	startReq := supSecret.buildStartReq(nil, nil)
	if startReq.SubscriptionRoute != "chatgpt" || startReq.BoundSecretEnv != "" {
		t.Errorf("startReq subscription=%q boundSecretEnv=%q", startReq.SubscriptionRoute, startReq.BoundSecretEnv)
	}
	if len(startReq.BoundPlaceholders) != 1 || !strings.HasPrefix(startReq.BoundPlaceholders[0], "ACME_TOKEN=") {
		t.Errorf("BoundPlaceholders = %v, want only ACME_TOKEN", startReq.BoundPlaceholders)
	}

	// Malformed CODEX_AUTH_JSON fails with a Try: hint.
	_, _, _, err = supSecret.buildBoundCredentials(supSecret.boundSecretMap(), []string{"CODEX_AUTH_JSON"}, map[string]api.Secret{
		"CODEX_AUTH_JSON": "not-json",
	})
	if err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("malformed CODEX_AUTH_JSON err = %v, want Try: hint", err)
	}

	// 2. Host sign-in reads ~/.codex/auth.json and attaches AuthFile + LockFile.
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmpHome, "state"))
	codexDir := filepath.Join(tmpHome, ".codex")
	if err := os.MkdirAll(codexDir, 0o700); err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(codexDir, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"host-acc","refresh_token":"host-ref","account_id":"acct_host"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	supHost := &Supervisor{
		id: "test-sub-host",
		m: &host.Manifest{
			ID:    "test-sub-host",
			Agent: host.AgentInfo{Kind: "codex"},
			Wall: &host.WallRecord{
				Driver:           "process",
				SubscriptionAuth: "host",
			},
		},
	}
	hcred, needed, err := supHost.hostSubscriptionCredential()
	if err != nil || !needed {
		t.Fatalf("hostSubscriptionCredential: needed=%v err=%v", needed, err)
	}
	if hcred.Route != "chatgpt" || string(hcred.Value) != "host-acc" || hcred.OAuth == nil ||
		hcred.OAuth.AccountID != "acct_host" || hcred.OAuth.AuthFile != authPath || hcred.OAuth.LockFile == "" {
		t.Fatalf("unexpected host subscription cred: %+v", hcred)
	}
}
