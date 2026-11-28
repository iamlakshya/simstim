package agents

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Each first-class agent gives the form of its own session IDs, and each
// form in the table is a regular expression. An ID passes only as a whole.
// An agent without a form takes no ID.
func TestValidID(t *testing.T) {
	for _, a := range Table {
		if a.IDPattern == "" {
			if a.Support == FirstClass {
				t.Errorf("%s: a first-class agent needs the form of its session IDs", a.Kind)
			}
			continue
		}
		if _, err := regexp.Compile(a.IDPattern); err != nil {
			t.Errorf("%s: %v", a.Kind, err)
		}
	}
	const uuid4, uuid7 = "3f2b8c1e-9d4a-4e6b-8f7c-1a2b3c4d5e6f", "0199a3f1-7c2e-7d4b-9a5e-3f1c2b4d5e6f"
	for _, c := range []struct {
		kind, id string
		ok       bool
	}{
		// Claude Code's IDs are UUIDs, and Codex makes UUIDv7 IDs.
		{"claude", uuid4, true},
		{"claude", uuid7, true},
		{"codex", uuid7, true},
		{"codex", uuid4, true},
		{"claude", "--dangerously-skip-permissions", false},
		{"claude", "--permission-mode=bypassPermissions", false},
		{"claude", "", false},
		{"claude", strings.ToUpper(uuid4), false},
		{"claude", uuid4 + "\n", false},
		{"claude", uuid4 + " --yolo", false},
		{"claude", "-" + uuid4, false},
		{"claude", uuid4[:35], false},
		{"claude", "my-session", false},
		{"codex", "--yolo", false},
		{"codex", "-p", false},
		{"codex", "session_" + uuid4, false},
		// Without a form, no ID passes.
		{"opencode", uuid4, false},
		{"shell", uuid4, false},
	} {
		a, ok := Find(c.kind)
		if !ok {
			t.Fatalf("no agent %s", c.kind)
		}
		if got := a.ValidID(c.id); got != c.ok {
			t.Errorf("%s: ValidID(%q) = %v, want %v", c.kind, c.id, got, c.ok)
		}
	}
	// A form without anchors still has to match the whole ID.
	if a := (Agent{IDPattern: "[0-9]+"}); a.ValidID("12 --yolo") || !a.ValidID("12") {
		t.Error("a form without anchors matched part of an ID")
	}
	if a := (Agent{IDPattern: "("}); a.ValidID("(") {
		t.Error("a form that does not compile took an ID")
	}
}

// Codex takes its session ID as a positional argument, so its resume
// command ends the options with "--" before it.
func TestCodexResumeEndsTheOptions(t *testing.T) {
	a, _ := Find("codex")
	got := Expand(a.Resume, map[string]string{"agent_session_id": "--yolo"})
	if want := []string{"codex", "--no-daemon", "resume", "--", "--yolo"}; !slices.Equal(got, want) {
		t.Errorf("resume = %q, want %q", got, want)
	}
}

func TestEveryAgentHasASupportLevel(t *testing.T) {
	seen := map[string]bool{}
	for i, a := range Table {
		switch a.Support {
		case FirstClass, Supported, BestEffort:
		default:
			t.Errorf("%s: support level %q", a.Kind, a.Support)
		}
		if seen[a.Kind] {
			t.Errorf("%s: twice in the table", a.Kind)
		}
		seen[a.Kind] = true
		if len(a.Launch) == 0 || len(a.Binaries) == 0 {
			t.Errorf("%s: no launch command or binary", a.Kind)
		}
		if i > 0 && rank(a.Support) < rank(Table[i-1].Support) && a.Kind != "shell" {
			t.Errorf("%s (%s) comes after %s (%s): list the more supported agents first", a.Kind, a.Support, Table[i-1].Kind, Table[i-1].Support)
		}
	}
}

func TestFirstClassAgents(t *testing.T) {
	// Claude Code and Codex are first-class from day one: hooks for status,
	// and resume after a reboot.
	for _, k := range []string{"claude", "codex"} {
		a, ok := Find(k)
		if !ok || a.Support != FirstClass {
			t.Fatalf("%s: want first-class, got %+v", k, a.Support)
		}
		if len(a.Hooks) == 0 || len(a.Resume) == 0 || len(a.Continue) == 0 {
			t.Errorf("%s: a first-class agent needs hooks, resume and continue", k)
		}
	}
}

func TestAntigravity(t *testing.T) {
	a, ok := Find("antigravity")
	if !ok || a.Binaries[0] != "agy" {
		t.Fatalf("antigravity: %+v", a)
	}
	got := Expand(a.Resume, map[string]string{"agent_session_id": "c-123"})
	if want := []string{"agy", "--conversation", "c-123"}; len(got) != 3 || got[2] != want[2] {
		t.Errorf("resume = %v, want %v", got, want)
	}
}

// The memory that each agent's own docs ask for, where they name it.
func TestMemory(t *testing.T) {
	want := map[string]int64{"claude": 4 * GB, "codex": 4 * GB}
	for _, a := range Table {
		if a.Memory != want[a.Kind] {
			t.Errorf("%s asks for %d bytes, want %d", a.Kind, a.Memory, want[a.Kind])
		}
	}
}

func rank(s Support) int {
	return map[Support]int{FirstClass: 0, Supported: 1, BestEffort: 2}[s]
}

func TestEveryCheckedAgentCanBeInstalled(t *testing.T) {
	for _, a := range Table {
		if (a.Support == FirstClass || a.Support == Supported) && a.Kind != "shell" {
			if a.Setup == nil || a.Setup.Install == "" || a.Setup.Source == "" || a.Setup.Login == "" {
				t.Errorf("%s: a first-class or supported agent needs an install command, its source and a login", a.Kind)
			}
		}
	}
}

func TestFirstClassEgressHosts(t *testing.T) {
	claude, ok := Find("claude")
	if !ok {
		t.Fatal("claude agent missing")
	}
	wantClaude := []string{"api.anthropic.com", "platform.claude.com", "claude.ai", "claude.com", "sso.claude.com"}
	if !slices.Equal(claude.Egress, wantClaude) {
		t.Errorf("claude.Egress = %q, want %q", claude.Egress, wantClaude)
	}
	for _, forbidden := range []string{"downloads.claude.ai", "mcp-proxy.anthropic.com"} {
		if slices.Contains(claude.Egress, forbidden) {
			t.Errorf("claude.Egress must not include %q", forbidden)
		}
	}

	codex, ok := Find("codex")
	if !ok {
		t.Fatal("codex agent missing")
	}
	wantCodex := []string{"api.openai.com", "chatgpt.com", "auth.openai.com"}
	if !slices.Equal(codex.Egress, wantCodex) {
		t.Errorf("codex.Egress = %q, want %q", codex.Egress, wantCodex)
	}
}

func TestFirstClassModelGatewayAndGuard(t *testing.T) {
	hosts := ModelHosts()
	wantHosts := []string{"api.anthropic.com", "api.openai.com", "chatgpt.com"}
	if !slices.Equal(hosts, wantHosts) {
		t.Fatalf("ModelHosts() = %q, want %q", hosts, wantHosts)
	}

	claude, _ := Find("claude")
	if claude.ModelGateway == nil || claude.ModelGateway.Route != "anthropic" ||
		claude.ModelGateway.BaseURLEnv != "ANTHROPIC_BASE_URL" ||
		claude.ModelGateway.UpstreamHost() != "api.anthropic.com" {
		t.Fatalf("claude.ModelGateway = %+v", claude.ModelGateway)
	}
	if claude.ModelGuard == nil || claude.ModelGuard.Wire != "anthropic-messages" {
		t.Fatalf("claude.ModelGuard = %+v", claude.ModelGuard)
	}
	if slices.Contains(claude.ModelGuard.Paths, "POST /v1/messages/batches") {
		t.Error("claude.ModelGuard.Paths must never include /v1/messages/batches")
	}
	if b, ok := FindSecretBinding(claude, "CLAUDE_CODE_OAUTH_TOKEN"); !ok {
		t.Error("missing CLAUDE_CODE_OAUTH_TOKEN binding")
	} else if h, v := b.FormatHeader("tok"); h != "Authorization" || v != "Bearer tok" {
		t.Errorf("FormatHeader = (%q, %q), want (Authorization, Bearer tok)", h, v)
	}

	codex, _ := Find("codex")
	if codex.ModelGateway == nil || codex.ModelGateway.Route != "openai" ||
		codex.ModelGateway.BaseURLPath != "/v1" ||
		!strings.Contains(codex.ModelGateway.ConfigTmpl, "supports_websockets = false") {
		t.Fatalf("codex.ModelGateway = %+v", codex.ModelGateway)
	}
	if codex.ModelGuard == nil || codex.ModelGuard.Wire != "openai-responses" {
		t.Fatalf("codex.ModelGuard = %+v", codex.ModelGuard)
	}
	if codex.SubscriptionGateway == nil || codex.SubscriptionGateway.Route != "chatgpt" ||
		codex.SubscriptionGateway.UpstreamHost() != "chatgpt.com" ||
		codex.SubscriptionGateway.SecretEnv != "CODEX_AUTH_JSON" ||
		codex.SubscriptionGateway.AccountHeader != "Chatgpt-Account-Id" ||
		codex.SubscriptionGateway.Guard == nil ||
		!slices.Contains(codex.SubscriptionGateway.Guard.Paths, "POST /backend-api/codex/responses") {
		t.Fatalf("codex.SubscriptionGateway = %+v", codex.SubscriptionGateway)
	}
	if route, ok := FindGatewayRouteForHost("chatgpt.com"); !ok || route != "chatgpt" {
		t.Fatalf("FindGatewayRouteForHost(chatgpt.com) = (%q, %v), want (chatgpt, true)", route, ok)
	}
}

func TestAgentsDiskDirsAndSandboxEnv(t *testing.T) {
	claude, ok := Find("claude")
	if !ok {
		t.Fatal("claude agent missing")
	}
	if !slices.Contains(claude.InstallDirs, ".local/share/claude") {
		t.Errorf("claude.InstallDirs = %q, want .local/share/claude", claude.InstallDirs)
	}
	if claude.SandboxEnv["DISABLE_AUTOUPDATER"] != "1" {
		t.Errorf("claude.SandboxEnv = %v, want DISABLE_AUTOUPDATER=1", claude.SandboxEnv)
	}

	codex, ok := Find("codex")
	if !ok {
		t.Fatal("codex agent missing")
	}
	if !slices.Contains(codex.InstallDirs, ".codex/packages") {
		t.Errorf("codex.InstallDirs = %q, want .codex/packages", codex.InstallDirs)
	}
	if codex.SandboxEnv["NO_UPDATE_NOTIFIER"] != "1" {
		t.Errorf("codex.SandboxEnv = %v, want NO_UPDATE_NOTIFIER=1", codex.SandboxEnv)
	}

	opencode, ok := Find("opencode")
	if !ok {
		t.Fatal("opencode agent missing")
	}
	if !slices.Contains(opencode.InstallDirs, ".opencode") {
		t.Errorf("opencode.InstallDirs = %q, want .opencode", opencode.InstallDirs)
	}
}
