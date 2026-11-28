package host

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
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

	// 8. Custom secret cannot hijack the built-in "chatgpt" subscription route.
	hijackChatGPT := map[string]api.SecretBind{
		"ACME_CHAT": {Name: "ACME_CHAT", Hosts: []string{"api.acme.example"}, Route: "chatgpt"},
	}
	_, _, err = resolveProcessSecrets(shell, true, []string{"ACME_CHAT"}, hijackChatGPT, nil)
	assertBadSecret(t, "hijack chatgpt route", err, "collides")
}

func TestCodexSubscriptionAuthDetectionAndBinding(t *testing.T) {
	codex, ok := agents.Find("codex")
	if !ok {
		t.Fatal("codex agent missing")
	}

	// 1. CODEX_AUTH_JSON auto-binds to chatgpt.com on the built-in "chatgpt" route.
	bound, exposed, err := resolveProcessSecrets(codex, true, []string{"CODEX_AUTH_JSON"}, nil, nil)
	if err != nil {
		t.Fatalf("resolve CODEX_AUTH_JSON: %v", err)
	}
	if len(exposed) != 0 || len(bound) != 1 || bound[0].Route != "chatgpt" || bound[0].Hosts[0] != "chatgpt.com" {
		t.Fatalf("CODEX_AUTH_JSON bound=%+v exposed=%v", bound, exposed)
	}
	if got := detectSubscriptionAuth(codex, true, bound, exposed); got != "secret:CODEX_AUTH_JSON" {
		t.Errorf("detectSubscriptionAuth(CODEX_AUTH_JSON) = %q, want secret:CODEX_AUTH_JSON", got)
	}

	// 2. Host ~/.codex/auth.json with auth_mode="chatgpt" selects "host" when no API key is passed.
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	if got := detectSubscriptionAuth(codex, true, nil, nil); got != "" {
		t.Errorf("detectSubscriptionAuth without auth.json = %q, want empty", got)
	}
	codexDir := filepath.Join(tmpHome, ".codex")
	if err := os.MkdirAll(codexDir, 0o700); err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(codexDir, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"tok","refresh_token":"rt","account_id":"acct_1"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := detectSubscriptionAuth(codex, true, nil, nil); got != "host" {
		t.Errorf("detectSubscriptionAuth with host auth.json = %q, want host", got)
	}

	// 3. Explicit OPENAI_API_KEY (bound or exposed) takes precedence over host ChatGPT login.
	apiBound, _, err := resolveProcessSecrets(codex, true, []string{"OPENAI_API_KEY"}, nil, nil)
	if err != nil {
		t.Fatalf("resolve OPENAI_API_KEY: %v", err)
	}
	if got := detectSubscriptionAuth(codex, true, apiBound, nil); got != "" {
		t.Errorf("detectSubscriptionAuth with bound OPENAI_API_KEY = %q, want empty", got)
	}
	if got := detectSubscriptionAuth(codex, true, nil, []string{"OPENAI_API_KEY"}); got != "" {
		t.Errorf("detectSubscriptionAuth with exposed OPENAI_API_KEY = %q, want empty", got)
	}

	// 4. auth.json with auth_mode="apikey" does NOT select ChatGPT subscription mode.
	if err := os.WriteFile(authPath, []byte(`{"auth_mode":"apikey","OPENAI_API_KEY":"sk-test"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := detectSubscriptionAuth(codex, true, nil, nil); got != "" {
		t.Errorf("detectSubscriptionAuth with apikey auth.json = %q, want empty", got)
	}
}

func TestStartDeferredRecomputesSubscriptionAuth(t *testing.T) {
	runCase := func(
		t *testing.T,
		name string,
		hostAuth bool,
		initialSubAuth string,
		args api.SessionStartArgs,
		wantSubAuth string,
		wantBound []string,
		wantExposed []string,
	) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			tmpHome := t.TempDir()
			t.Setenv("HOME", tmpHome)
			t.Setenv("XDG_STATE_HOME", filepath.Join(tmpHome, "state"))
			t.Setenv("XDG_RUNTIME_DIR", filepath.Join(tmpHome, "run"))
			if hostAuth {
				codexDir := filepath.Join(tmpHome, ".codex")
				if err := os.MkdirAll(codexDir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(codexDir, "auth.json"),
					[]byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"host-tok","refresh_token":"host-rt","account_id":"acct_host"}}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			id := NewID()
			now := time.Now().UTC()
			m := &Manifest{
				Schema:    1,
				ID:        id,
				Name:      "deferred-codex",
				CreatedAt: now,
				Agent:     AgentInfo{Kind: "codex", Argv: []string{"codex"}},
				Cwd:       "/work/repo",
				Run:       Runtime{Socket: paths.Socket(id)},
				Status:    Status{State: StateStarting, Since: now},
				Wall: &WallRecord{
					Level:            "process",
					Driver:           "process",
					WorkspaceHost:    filepath.Join(tmpHome, "repo"),
					WorkspaceGuest:   "/work/repo",
					SubscriptionAuth: initialSubAuth,
				},
			}
			if err := m.Save(); err != nil {
				t.Fatal(err)
			}

			var launched *Manifest
			prev := startDeferredSession
			startDeferredSession = func(lm *Manifest, _ map[string]api.Secret) (*SessionInfo, error) {
				launched = lm
				info := infoFrom(lm, true)
				return &info, nil
			}
			t.Cleanup(func() { startDeferredSession = prev })

			args.ID = id
			if _, err := StartDeferred(args); err != nil {
				t.Fatalf("StartDeferred: %v", err)
			}
			if launched == nil || launched.Wall == nil {
				t.Fatal("startDeferredSession was not called with a walled manifest")
			}
			if launched.Wall.SubscriptionAuth != wantSubAuth {
				t.Errorf("SubscriptionAuth = %q, want %q", launched.Wall.SubscriptionAuth, wantSubAuth)
			}
			var gotBound []string
			for _, b := range launched.Wall.BoundSecrets {
				gotBound = append(gotBound, b.Name)
			}
			if strings.Join(gotBound, ",") != strings.Join(wantBound, ",") {
				t.Errorf("BoundSecrets = %v, want %v", gotBound, wantBound)
			}
			if strings.Join(launched.Wall.ExposedSecrets, ",") != strings.Join(wantExposed, ",") {
				t.Errorf("ExposedSecrets = %v, want %v", launched.Wall.ExposedSecrets, wantExposed)
			}
		})
	}

	const sampleAuth = `{"auth_mode":"chatgpt","tokens":{"access_token":"laptop-tok","refresh_token":"laptop-rt","account_id":"acct_laptop"}}`

	runCase(t, "codex_auth_json_without_host_file", false, "",
		api.SessionStartArgs{Secrets: map[string]api.Secret{"CODEX_AUTH_JSON": sampleAuth}},
		"secret:CODEX_AUTH_JSON", []string{"CODEX_AUTH_JSON"}, nil)

	runCase(t, "codex_auth_json_overrides_host_file", true, "host",
		api.SessionStartArgs{Secrets: map[string]api.Secret{"CODEX_AUTH_JSON": sampleAuth}},
		"secret:CODEX_AUTH_JSON", []string{"CODEX_AUTH_JSON"}, nil)

	runCase(t, "bound_openai_api_key_overrides_host_file", true, "host",
		api.SessionStartArgs{Secrets: map[string]api.Secret{"OPENAI_API_KEY": "sk-proj-test"}},
		"", []string{"OPENAI_API_KEY"}, nil)

	runCase(t, "exposed_openai_api_key_overrides_host_file", true, "host",
		api.SessionStartArgs{
			Secrets:        map[string]api.Secret{"OPENAI_API_KEY": "sk-proj-test"},
			ExposedSecrets: []string{"OPENAI_API_KEY"},
		},
		"", nil, []string{"OPENAI_API_KEY"})

	runCase(t, "no_secrets_recomputes_host_file", true, "",
		api.SessionStartArgs{},
		"host", nil, nil)
}
