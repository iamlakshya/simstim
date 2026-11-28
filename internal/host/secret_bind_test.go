package host

import (
	"errors"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestResolveProcessSecrets(t *testing.T) {
	claude, ok := agents.Find("claude")
	if !ok {
		t.Fatal("claude agent missing")
	}
	codex, ok := agents.Find("codex")
	if !ok {
		t.Fatal("codex agent missing")
	}
	shell, ok := agents.Find("shell")
	if !ok {
		t.Fatal("shell agent missing")
	}

	// 1. Agent-table secret binds automatically without explicit [secrets.NAME] hosts.
	bound, exposed, err := resolveProcessSecrets(claude, true, []string{"ANTHROPIC_API_KEY"}, nil, nil)
	if err != nil {
		t.Fatalf("claude ANTHROPIC_API_KEY: %v", err)
	}
	if len(exposed) != 0 || len(bound) != 1 || bound[0].Name != "ANTHROPIC_API_KEY" || bound[0].Header != "x-api-key" || bound[0].Route != "anthropic" {
		t.Fatalf("claude bound=%+v exposed=%v", bound, exposed)
	}

	// 2. Codex OPENAI_API_KEY binds to openai with Authorization: Bearer {value}.
	bound, exposed, err = resolveProcessSecrets(codex, true, []string{"OPENAI_API_KEY"}, nil, nil)
	if err != nil {
		t.Fatalf("codex OPENAI_API_KEY: %v", err)
	}
	if len(exposed) != 0 || len(bound) != 1 || bound[0].Header != "Authorization" || bound[0].Format != "Bearer {value}" || bound[0].Route != "openai" {
		t.Fatalf("codex bound=%+v exposed=%v", bound, exposed)
	}

	// 3. Custom [secrets.NAME] with hosts binds with default Authorization: Bearer.
	userBinds := map[string]api.SecretBind{
		"ACME_TOKEN": {Name: "ACME_TOKEN", Hosts: []string{"api.acme.example"}},
	}
	bound, exposed, err = resolveProcessSecrets(shell, true, []string{"ACME_TOKEN"}, userBinds, nil)
	if err != nil {
		t.Fatalf("custom ACME_TOKEN: %v", err)
	}
	if len(exposed) != 0 || len(bound) != 1 || bound[0].Header != "Authorization" || bound[0].Format != "Bearer {value}" || bound[0].Hosts[0] != "api.acme.example" {
		t.Fatalf("custom bound=%+v exposed=%v", bound, exposed)
	}

	// 4. Unbound secret without --expose fails closed with secret_unbound.
	_, _, err = resolveProcessSecrets(claude, true, []string{"GITHUB_TOKEN"}, nil, nil)
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != "secret_unbound" || !strings.Contains(pe.Hint, "--expose GITHUB_TOKEN") {
		t.Fatalf("unbound secret err = %v, want secret_unbound with --expose hint", err)
	}

	// 5. Explicit --expose allows an unbound secret into exposedOut.
	bound, exposed, err = resolveProcessSecrets(claude, true, []string{"ANTHROPIC_API_KEY", "GITHUB_TOKEN"}, nil, []string{"GITHUB_TOKEN"})
	if err != nil {
		t.Fatalf("mixed bound+exposed: %v", err)
	}
	if len(bound) != 1 || bound[0].Name != "ANTHROPIC_API_KEY" || strings.Join(exposed, ",") != "GITHUB_TOKEN" {
		t.Fatalf("mixed bound=%+v exposed=%v", bound, exposed)
	}

	// 6. Shell session still binds known agent secrets via FindSecretBindingAnywhere.
	bound, exposed, err = resolveProcessSecrets(shell, true, []string{"ANTHROPIC_API_KEY"}, nil, nil)
	if err != nil {
		t.Fatalf("shell ANTHROPIC_API_KEY: %v", err)
	}
	if len(exposed) != 0 || len(bound) != 1 || bound[0].Route != "anthropic" {
		t.Fatalf("shell bound=%+v exposed=%v", bound, exposed)
	}
}

func TestResolveProcessSecretsRejectsRouteCollisionsAndBadFormat(t *testing.T) {
	claude, ok := agents.Find("claude")
	if !ok {
		t.Fatal("claude agent missing")
	}
	shell, ok := agents.Find("shell")
	if !ok {
		t.Fatal("shell agent missing")
	}

	assertBadSecret := func(t *testing.T, label string, err error, wantSubstr string) {
		t.Helper()
		var pe *proto.Error
		if !errors.As(err, &pe) || pe.Code != "bad_secret" {
			t.Fatalf("%s: err = %v, want bad_secret", label, err)
		}
		if wantSubstr != "" && !strings.Contains(pe.Message, wantSubstr) {
			t.Errorf("%s: message = %q, want substring %q", label, pe.Message, wantSubstr)
		}
		if pe.Hint == "" {
			t.Errorf("%s: missing Try: hint", label)
		}
	}

	// 1. Custom secret hijacking built-in "anthropic" route on claude or shell.
	hijackBinds := map[string]api.SecretBind{
		"ACME_TOKEN": {Name: "ACME_TOKEN", Hosts: []string{"api.acme.example"}, Route: "anthropic"},
	}
	_, _, err := resolveProcessSecrets(claude, true, []string{"ACME_TOKEN"}, hijackBinds, nil)
	assertBadSecret(t, "hijack anthropic on claude", err, "collides")
	_, _, err = resolveProcessSecrets(shell, true, []string{"ACME_TOKEN"}, hijackBinds, nil)
	assertBadSecret(t, "hijack anthropic on shell", err, "collides")

	// 2. Two custom secrets claiming the same explicit route for different hosts.
	dupRouteBinds := map[string]api.SecretBind{
		"ACME_A": {Name: "ACME_A", Hosts: []string{"a.acme.example"}, Route: "shared"},
		"ACME_B": {Name: "ACME_B", Hosts: []string{"b.acme.example"}, Route: "shared"},
	}
	_, _, err = resolveProcessSecrets(shell, true, []string{"ACME_A", "ACME_B"}, dupRouteBinds, nil)
	assertBadSecret(t, "duplicate explicit route", err, "collides")

	// 3. Derived-slug collision across two secrets: "a-b.example" vs "a.b.example".
	derivedBinds := map[string]api.SecretBind{
		"FIRST_KEY":  {Name: "FIRST_KEY", Hosts: []string{"a-b.example"}},
		"SECOND_KEY": {Name: "SECOND_KEY", Hosts: []string{"a.b.example"}},
	}
	_, _, err = resolveProcessSecrets(shell, true, []string{"FIRST_KEY", "SECOND_KEY"}, derivedBinds, nil)
	assertBadSecret(t, "derived slug collision across secrets", err, "collides")

	// 4. Derived-slug collision inside one multi-host secret.
	intraBinds := map[string]api.SecretBind{
		"PAIR_KEY": {Name: "PAIR_KEY", Hosts: []string{"a-b.example", "a.b.example"}},
	}
	_, _, err = resolveProcessSecrets(shell, true, []string{"PAIR_KEY"}, intraBinds, nil)
	assertBadSecret(t, "derived slug collision inside secret", err, "collide")

	// 5. Two different secrets targeting the same host/route.
	sameHostBinds := map[string]api.SecretBind{
		"ACME_1": {Name: "ACME_1", Hosts: []string{"api.acme.example"}},
		"ACME_2": {Name: "ACME_2", Hosts: []string{"api.acme.example"}},
	}
	_, _, err = resolveProcessSecrets(shell, true, []string{"ACME_1", "ACME_2"}, sameHostBinds, nil)
	assertBadSecret(t, "two secrets same custom host", err, "both claim")
	_, _, err = resolveProcessSecrets(claude, true, []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"}, nil, nil)
	assertBadSecret(t, "two secrets same anthropic route", err, "both claim")

	// 6. Explicit route paired with multiple hosts is refused.
	multiRouteBinds := map[string]api.SecretBind{
		"MULTI": {Name: "MULTI", Hosts: []string{"a.acme.example", "b.acme.example"}, Route: "acme"},
	}
	_, _, err = resolveProcessSecrets(shell, true, []string{"MULTI"}, multiRouteBinds, nil)
	assertBadSecret(t, "explicit route with multiple hosts", err, "multiple hosts")

	// 7. CR/LF in format is refused.
	badFmtBinds := map[string]api.SecretBind{
		"BAD_FMT": {Name: "BAD_FMT", Hosts: []string{"api.acme.example"}, Format: "Bearer {value}\r\nX-Inject: 1"},
	}
	_, _, err = resolveProcessSecrets(shell, true, []string{"BAD_FMT"}, badFmtBinds, nil)
	assertBadSecret(t, "CR/LF in format", err, "CR or LF")
}
