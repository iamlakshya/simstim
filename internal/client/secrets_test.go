package client

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

const testSecret = "fake-secret-for-tests-9876543210"

func TestFetchSecrets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the commands are sh")
	}
	var s Settings
	s.Secrets = map[string]any{
		"OPENAI_API_KEY": "printf '%s\\n' " + testSecret,
		"MULTI":          "printf 'line one\\nline two\\r\\n'",
	}
	got, err := FetchSecrets(context.Background(), s, []string{"OPENAI_API_KEY", "MULTI"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if string(got["OPENAI_API_KEY"]) != testSecret || string(got["MULTI"]) != "line one\nline two" {
		t.Fatalf("values: %q %q", string(got["OPENAI_API_KEY"]), string(got["MULTI"]))
	}
	if strings.Contains(fmt.Sprint(got), testSecret) {
		t.Fatal("the values print")
	}
}

func TestFetchSecretsErrorsShowNoValue(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the commands are sh")
	}
	old := SecretTimeout
	SecretTimeout = 300 * time.Millisecond
	defer func() { SecretTimeout = old }()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var s Settings
	s.Secrets = map[string]any{
		"FAILS":   "echo " + testSecret + "; echo " + testSecret + " >&2; exit 3",
		"EMPTY":   "true",
		"SLOW":    "echo " + testSecret + "; sleep 5",
		"NUL":     "printf '" + testSecret + "\\000'",
		"TOO_BIG": "head -c 70000 /dev/zero | tr '\\0' x",
	}
	for name, want := range map[string]string{
		"FAILS":   "the command of the secret FAILS failed (exit code 3)",
		"EMPTY":   "the command of the secret EMPTY printed nothing",
		"SLOW":    "the command of the secret SLOW did not finish in 300ms",
		"NUL":     "the command of the secret NUL printed a NUL byte",
		"TOO_BIG": "the command of the secret TOO_BIG printed more than 64 KiB",
		"MISSING": "no secret named MISSING in config.toml",
		"BAD-ONE": `"BAD-ONE" cannot name a secret`,
	} {
		_, err := FetchSecrets(context.Background(), s, []string{name}, false)
		var ue *UserError
		if !errors.As(err, &ue) || ue.Msg != want {
			t.Errorf("%s: %v, want %q", name, err, want)
			continue
		}
		if strings.Contains(ue.Msg+ue.Hint, testSecret) {
			t.Errorf("%s: the error shows the value: %+v", name, ue)
		}
		if want := "Add MISSING = \"<the command that prints it>\" under [secrets] in " + settingsPath() + "."; name == "MISSING" && ue.Hint != want {
			t.Errorf("MISSING: hint %q, want %q", ue.Hint, want)
		}
	}
}

func TestSecretNames(t *testing.T) {
	h := Host{Name: "h", Secrets: []string{"A", "B"}}
	if got := strings.Join(SecretNames(h, []string{"B", "C", " "}), ","); got != "A,B,C" {
		t.Fatalf("names %s", got)
	}
}

func TestRequireSecrets(t *testing.T) {
	c := &Conn{Host: Host{Name: "old"}}
	if err := c.RequireSecrets(NewArgs{}); err != nil {
		t.Fatal(err)
	}
	if err := c.RequireSecrets(NewArgs{Secrets: map[string]api.Secret{"A": "x"}}); err == nil {
		t.Fatal("an old helper got secrets it would drop")
	}
	c.Features = []string{"secrets"}
	if err := c.RequireSecrets(NewArgs{Secrets: map[string]api.Secret{"A": "x"}}); err != nil {
		t.Fatal(err)
	}
}

func TestSecretTableConfigDecodeAndBind(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the commands are sh")
	}
	rawTOML := `
[secrets]
PLAIN_KEY = "printf 'plain-val'"

[secrets.ANTHROPIC_API_KEY]
run = "printf 'ant-val'"
hosts = ["API.Anthropic.com."]
header = "x-api-key"
format = "{value}"

[secrets.CUSTOM_API_KEY]
command = "printf 'custom-val'"
hosts = ["api.acme.example"]
header = "X-Acme-Key"
route = "acme"

[secrets.MULTI_HOST_KEY]
run = "printf 'multi-val'"
hosts = ["api.acme.example", "uploads.acme.example"]
header = "X-Multi-Key"
`
	var s Settings
	strictErr, _, err := DecodeConfigStrict("config.toml", []byte(rawTOML), &s)
	if err != nil || strictErr != nil {
		t.Fatalf("DecodeConfigStrict: err=%v strict=%v", err, strictErr)
	}

	got, err := FetchSecrets(context.Background(), s, []string{"PLAIN_KEY", "ANTHROPIC_API_KEY", "CUSTOM_API_KEY", "MULTI_HOST_KEY"}, false)
	if err != nil {
		t.Fatalf("FetchSecrets: %v", err)
	}
	if string(got["PLAIN_KEY"]) != "plain-val" || string(got["ANTHROPIC_API_KEY"]) != "ant-val" || string(got["CUSTOM_API_KEY"]) != "custom-val" || string(got["MULTI_HOST_KEY"]) != "multi-val" {
		t.Fatalf("unexpected fetched values: %v", got)
	}

	binds := SecretBindsFor(s, []string{"PLAIN_KEY", "ANTHROPIC_API_KEY", "CUSTOM_API_KEY", "MULTI_HOST_KEY"})
	if _, hasPlain := binds["PLAIN_KEY"]; hasPlain {
		t.Fatalf("plain secret should not appear in SecretBindsFor: %+v", binds)
	}
	ab, ok := binds["ANTHROPIC_API_KEY"]
	if !ok || len(ab.Hosts) != 1 || ab.Hosts[0] != "api.anthropic.com" || ab.Header != "x-api-key" {
		t.Fatalf("unexpected ANTHROPIC_API_KEY bind: %+v", ab)
	}
	cb, ok := binds["CUSTOM_API_KEY"]
	if !ok || len(cb.Hosts) != 1 || cb.Hosts[0] != "api.acme.example" || cb.Route != "acme" {
		t.Fatalf("unexpected CUSTOM_API_KEY bind: %+v", cb)
	}
	mb, ok := binds["MULTI_HOST_KEY"]
	if !ok || len(mb.Hosts) != 2 || mb.Hosts[1] != "uploads.acme.example" || mb.Route != "" {
		t.Fatalf("unexpected MULTI_HOST_KEY bind: %+v", mb)
	}

	// Reject CR/LF in format and explicit route with multiple hosts.
	if _, err := ParseSecretConfig("BAD_FMT", map[string]any{
		"run": "echo x", "hosts": []any{"api.example.com"}, "format": "Bearer {value}\r\nX-Evil: 1",
	}); err == nil {
		t.Error("expected error for CR/LF in format")
	}
	if _, err := ParseSecretConfig("BAD_ROUTE", map[string]any{
		"run": "echo x", "hosts": []any{"a.example.com", "b.example.com"}, "route": "shared",
	}); err == nil {
		t.Error("expected error for explicit route with multiple hosts")
	}
}

func TestGatewaySecretNamesAutoBindsAgentSecret(t *testing.T) {
	var s Settings
	s.Secrets = map[string]any{
		"ANTHROPIC_API_KEY":       "echo a",
		"CLAUDE_CODE_OAUTH_TOKEN": "echo o",
		"OPENAI_API_KEY":          "echo x",
		"EXTRA":                   "echo e",
	}
	h := Host{Name: "devbox"}

	// Walled claude session auto-selects first matching agent secret from config.toml.
	got := GatewaySecretNames(s, h, "claude", "process", nil, nil)
	if strings.Join(got, ",") != "ANTHROPIC_API_KEY" {
		t.Fatalf("claude process wall auto secrets = %v, want [ANTHROPIC_API_KEY]", got)
	}

	// Explicit --secret CLAUDE_CODE_OAUTH_TOKEN does not also add ANTHROPIC_API_KEY.
	got = GatewaySecretNames(s, h, "claude", "process", []string{"CLAUDE_CODE_OAUTH_TOKEN"}, nil)
	if strings.Join(got, ",") != "CLAUDE_CODE_OAUTH_TOKEN" {
		t.Fatalf("claude explicit oauth = %v, want [CLAUDE_CODE_OAUTH_TOKEN]", got)
	}

	// Walled codex session auto-selects OPENAI_API_KEY and keeps --expose names.
	got = GatewaySecretNames(s, h, "codex", "process", nil, []string{"EXTRA"})
	if strings.Join(got, ",") != "EXTRA,OPENAI_API_KEY" {
		t.Fatalf("codex process wall secrets = %v, want [EXTRA OPENAI_API_KEY]", got)
	}

	// Unwalled session does NOT auto-select agent secrets.
	got = GatewaySecretNames(s, h, "claude", "none", nil, nil)
	if len(got) != 0 {
		t.Fatalf("unwalled session auto secrets = %v, want empty", got)
	}

	// Host with wall_floor="process" and empty wallLevel starts an unwalled
	// session in host.New, so GatewaySecretNames must NOT auto-fetch secrets.
	hFloor := Host{Name: "devbox", WallFloor: "process"}
	got = GatewaySecretNames(s, hFloor, "codex", "", nil, nil)
	if len(got) != 0 {
		t.Fatalf("empty wallLevel with host floor auto-fetched secrets = %v, want empty", got)
	}

	// Codex with only CODEX_AUTH_JSON configured auto-selects CODEX_AUTH_JSON.
	var sSub Settings
	sSub.Secrets = map[string]any{
		"CODEX_AUTH_JSON": "cat ~/.codex/auth.json",
	}
	got = GatewaySecretNames(sSub, h, "codex", "process", nil, nil)
	if strings.Join(got, ",") != "CODEX_AUTH_JSON" {
		t.Fatalf("codex subscription auto secret = %v, want [CODEX_AUTH_JSON]", got)
	}

	// Explicit --secret CODEX_AUTH_JSON does not also pull OPENAI_API_KEY.
	s.Secrets["CODEX_AUTH_JSON"] = "cat ~/.codex/auth.json"
	got = GatewaySecretNames(s, h, "codex", "process", []string{"CODEX_AUTH_JSON"}, nil)
	if strings.Join(got, ",") != "CODEX_AUTH_JSON" {
		t.Fatalf("codex explicit CODEX_AUTH_JSON = %v, want [CODEX_AUTH_JSON]", got)
	}
}
