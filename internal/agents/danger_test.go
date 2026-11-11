package agents

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// firstDanger returns the first run of argv that turns the agent's own
// approvals off, joined by spaces, or "" for none.
func firstDanger(a Agent, argv []string) string {
	sp := a.Dangers(argv)
	if len(sp) == 0 {
		return ""
	}
	return strings.Join(argv[sp[0].I:sp[0].I+sp[0].N], " ")
}

// esc writes a backslash for each "~" in s. The escape tests use it, so
// that the escapes in them stay escapes in the source.
func esc(s string) string { return strings.ReplaceAll(s, "~", `\`) }

// The commands that turn each first-class agent's own approvals off: its
// danger flags, with the values that they take, and an argument that holds
// its danger text, as written or as it decodes.
func TestDangers(t *testing.T) {
	const uuid = "3f2b8c1e-9d4a-4e6b-8f7c-1a2b3c4d5e6f"
	for _, c := range []struct {
		kind string
		argv []string
		want string // the first run refused, or "" when nothing is
	}{
		{"claude", []string{"--dangerously-skip-permissions"}, "--dangerously-skip-permissions"},
		{"claude", []string{"--dangerously-skip-permissions=true"}, "--dangerously-skip-permissions=true"},
		{"claude", []string{"--allow-dangerously-skip-permissions"}, "--allow-dangerously-skip-permissions"},
		{"claude", []string{"--permission-mode=bypassPermissions"}, "--permission-mode=bypassPermissions"},
		{"claude", []string{"--permission-mode", "bypassPermissions"}, "--permission-mode bypassPermissions"},
		{"claude", []string{"--permission-mode", "auto"}, "--permission-mode auto"},
		{"claude", []string{"--permission-mode=auto"}, "--permission-mode=auto"},
		{"claude", []string{"--permission-mode", "acceptEdits"}, "--permission-mode acceptEdits"},
		{"claude", []string{"--permission-mode", "dontAsk"}, "--permission-mode dontAsk"},
		{"claude", []string{"--permission-mode", "Plan"}, "--permission-mode Plan"}, // a safe value is exact
		{"claude", []string{"--permission-mode"}, "--permission-mode"},
		{"claude", []string{"--permission-mode", "plan"}, ""},
		{"claude", []string{"--permission-mode", "default"}, ""},
		{"claude", []string{"--permission-mode=manual"}, ""},
		// A list takes each value up to the next flag.
		{"claude", []string{"--allowedTools", "Bash"}, "--allowedTools Bash"},
		{"claude", []string{"--allowedTools", "Read", "Bash", "--continue"}, "--allowedTools Read Bash"},
		{"claude", []string{"--allowed-tools=Bash", "Read"}, "--allowed-tools=Bash Read"},
		{"claude", []string{"--allowedTools", "--continue"}, "--allowedTools"},
		{"claude", []string{"--add-dir", "../apps", "../lib"}, "--add-dir ../apps ../lib"},
		{"claude", []string{"--plugin-dir", "./plugin", "./other"}, "--plugin-dir ./plugin"},
		{"claude", []string{"--plugin-url", "https://example.com/p.zip"}, "--plugin-url https://example.com/p.zip"},
		// A path to a settings file: no check can read the file.
		{"claude", []string{"--settings", "./settings.json"}, "--settings ./settings.json"},
		{"claude", []string{"--settings=./settings.json"}, "--settings=./settings.json"},
		{"claude", []string{"--settings"}, "--settings"},
		{"claude", []string{"--settings", `{"model":"opus"}`}, ""},
		{"claude", []string{"--settings", `{"permissions":{"defaultMode":"auto"}}`}, `{"permissions":{"defaultMode":"auto"}}`},
		{"claude", []string{"--settings", `{"permissions":{"defaultMode":"plan"}}`}, ""},
		{"claude", []string{"--settings", `{"permissions":{"defaultMode":7}}`}, `{"permissions":{"defaultMode":7}}`},
		{"claude", []string{"--settings", `{"permissions":{"allow":["Bash"]}}`}, `{"permissions":{"allow":["Bash"]}}`},
		{"claude", []string{"--settings", `{"permissions":{"additionalDirectories":["/"]}}`}, `{"permissions":{"additionalDirectories":["/"]}}`},
		{"claude", []string{"--settings", `{"permissions":{"deny":["Bash"]}}`}, ""},
		// Sandboxed Bash runs without a prompt: autoAllowBashIfSandboxed is
		// true by default, so turning the sandbox on is enough.
		{"claude", []string{"--settings", `{"sandbox":{"autoAllowBashIfSandboxed":true}}`}, `{"sandbox":{"autoAllowBashIfSandboxed":true}}`},
		{"claude", []string{"--settings", `{"sandbox":{"enabled":true}}`}, `{"sandbox":{"enabled":true}}`},
		{"claude", []string{"--settings", `{"sandbox":{"enabled":true,"autoAllowBashIfSandboxed":true}}`},
			`{"sandbox":{"enabled":true,"autoAllowBashIfSandboxed":true}}`},
		{"claude", []string{"--settings", `{"sandbox":{"enabled":false,"autoAllowBashIfSandboxed":false}}`}, ""},
		{"claude", []string{"--settings", `{"hooks":{"PreToolUse":[]}}`}, `{"hooks":{"PreToolUse":[]}}`},
		{"claude", []string{"--settings", `{"hooks":{"PermissionRequest":[]}}`}, `{"hooks":{"PermissionRequest":[]}}`},
		{"claude", []string{`--settings={"permissions":{"defaultMode":"auto"}}`}, `--settings={"permissions":{"defaultMode":"auto"}}`},
		{"claude", []string{"--agents", `{"r":{"description":"d","prompt":"p","permissionMode":"auto"}}`},
			`{"r":{"description":"d","prompt":"p","permissionMode":"auto"}}`},
		{"claude", []string{"--agents", `{"r":{"description":"d","prompt":"p","permissionMode":"plan"}}`}, ""},
		{"claude", []string{"bypassPermissions"}, "bypassPermissions"},
		{"claude", []string{"--resume", uuid, "--continue"}, ""},
		{"claude", []string{"--yolo"}, ""}, // Codex's flag, not Claude Code's

		{"codex", []string{"--dangerously-bypass-approvals-and-sandbox"}, "--dangerously-bypass-approvals-and-sandbox"},
		{"codex", []string{"--yolo"}, "--yolo"},
		{"codex", []string{"--dangerously-bypass-hook-trust"}, "--dangerously-bypass-hook-trust"},
		{"codex", []string{"--approve-for-me"}, "--approve-for-me"},
		{"codex", []string{"--not-so-yolo"}, "--not-so-yolo"},
		{"codex", []string{"--profile", "work"}, "--profile work"},
		{"codex", []string{"--profile=work"}, "--profile=work"},
		{"codex", []string{"-p", "work"}, "-p work"},
		{"codex", []string{"-pwork"}, "-pwork"},
		{"codex", []string{"-a", "never"}, "-a never"},
		{"codex", []string{"-a=never"}, "-a=never"},
		{"codex", []string{"-anever"}, "-anever"},
		{"codex", []string{"-a", "NEVER"}, "-a NEVER"}, // a dangerous value may differ in case
		{"codex", []string{"--ask-for-approval", "never"}, "--ask-for-approval never"},
		{"codex", []string{"--ask-for-approval=never"}, "--ask-for-approval=never"},
		{"codex", []string{"-a", "on-request"}, ""},
		{"codex", []string{"--add-dir", "/srv", "/etc"}, "--add-dir /srv"}, // one value each time
		{"codex", []string{"--search"}, "--search"},
		{"codex", []string{"-s", "danger-full-access"}, "danger-full-access"},
		{"codex", []string{"-s", "workspace-write"}, ""},
		{"codex", []string{"-c", `sandbox_mode="danger-full-access"`}, `sandbox_mode="danger-full-access"`},
		{"codex", []string{"-c", `sandbox_mode="workspace-write"`}, `sandbox_mode="workspace-write"`},
		{"codex", []string{"-c", `approval_policy="never"`}, `approval_policy="never"`},
		{"codex", []string{"-c", `approvals_reviewer="auto_review"`}, `approvals_reviewer="auto_review"`},
		{"codex", []string{"-c", `default_permissions=":danger-full-access"`}, `default_permissions=":danger-full-access"`},
		{"codex", []string{"-c", `permissions.workspace.network.enabled=true`}, `permissions.workspace.network.enabled=true`},
		{"codex", []string{"-c", `permissions={ fs = { write = ["/"] } }`}, `permissions={ fs = { write = ["/"] } }`},
		{"codex", []string{"-c", `profile="work"`}, `profile="work"`},
		{"codex", []string{"-c", `profile=work`}, `profile=work`},
		{"codex", []string{"--config=profile=work"}, "--config=profile=work"},
		{"codex", []string{"-cprofile=work"}, "-cprofile=work"},
		{"codex", []string{"-c", `sandbox_workspace_write.network_access=true`}, `sandbox_workspace_write.network_access=true`},
		{"codex", []string{"-c", `sandbox_workspace_write={ writable_roots = ["/"] }`}, `sandbox_workspace_write={ writable_roots = ["/"] }`},
		// The -c forms of --search: web search other than the default.
		{"codex", []string{"-c", `web_search="live"`}, `web_search="live"`},
		{"codex", []string{"-c", `web_search=live`}, `web_search=live`},
		{"codex", []string{"-c", `web_search="indexed"`}, `web_search="indexed"`},
		{"codex", []string{"-c", `tools.web_search=true`}, `tools.web_search=true`},
		{"codex", []string{"-c", `tools.web_search={ context_size = "high" }`}, `tools.web_search={ context_size = "high" }`},
		{"codex", []string{"-c", `features.web_search=true`}, `features.web_search=true`},
		{"codex", []string{"-c", `features.web_search_request=true`}, `features.web_search_request=true`},
		{"codex", []string{"-c", `web_search="cached"`}, ""},
		{"codex", []string{"-c", `web_search="disabled"`}, ""},
		{"codex", []string{"-c", `features.web_search_request=false`}, ""},
		// The feature toggles: --enable FEATURE is -c features.FEATURE=true.
		{"codex", []string{"--enable", "web_search_request"}, "--enable web_search_request"},
		{"codex", []string{"--enable=web_search_request"}, "--enable=web_search_request"},
		{"codex", []string{"--enable", "web_search"}, "--enable web_search"},
		{"codex", []string{"--enable=web_search"}, "--enable=web_search"},
		{"codex", []string{"--enable", "web_search_cached"}, ""},
		{"codex", []string{"--enable", "undo"}, ""},
		{"codex", []string{"--disable", "web_search_request"}, ""},
		{"codex", []string{"--disable=web_search"}, ""},
		// The -c form of hook trust.
		{"codex", []string{"-c", `hooks.state.lint.trusted_hash="abc"`}, `hooks.state.lint.trusted_hash="abc"`},
		{"codex", []string{"-c", `model="gpt-5"`}, ""},
		{"codex", []string{"-c", `model_profile="work"`}, ""}, // a key named profile only
		{"codex", []string{"-c", `notify=["/home/u/bin/helper","hook","codex-notify"]`}, ""},
		{"codex", []string{"--no-daemon", "resume", "--", uuid, "--last", "--no-alt-screen", "--yolo-mode", "-c", "-s"}, ""},

		{"opencode", []string{"--yolo"}, ""}, // no danger data in its row yet
	} {
		a, _ := Find(c.kind)
		if got := firstDanger(a, c.argv); got != c.want {
			t.Errorf("%s %q: refused %q, want %q", c.kind, c.argv, got, c.want)
		}
	}
}

// The agents read JSON and TOML, so an escape cannot hide a danger: the
// check matches what an argument decodes to, as well as what it says. In
// each case, only the decoded argument holds the danger, so the check
// fails without the decoding.
func TestDangersDecodeEscapes(t *testing.T) {
	for _, c := range []struct {
		kind, arg string
	}{
		// The text bypassPermissions, only in a decoded string.
		{"claude", `{"permissions":{"defaultMode":"~u0062ypassPermissions"}}`},
		// The key defaultMode, only as a decoded key.
		{"claude", `{"permissions":{"~u0064efaultMode":"auto"}}`},
		// The path permissions.allow, only as a decoded key.
		{"claude", `{"permissions":{"~u0061llow":["Bash"]}}`},
		{"claude", `{"permissions":{"additional~u0044irectories":["/"]}}`},
		{"claude", `{"hooks":{"Pre~u0054oolUse":[]}}`},
		{"claude", `{"r":{"prompt":"p","permission~u004dode":"auto"}}`},
		// The sandbox keys, only as decoded keys.
		{"claude", `{"sandbox":{"autoAllow~u0042ashIfSandboxed":true}}`},
		{"claude", `{"~u0073andbox":{"enabled":true}}`},
		// The text danger-full-access, only in a decoded TOML value, under
		// a key that no rule names.
		{"codex", `x="danger~u002dfull~u002daccess"`},
		{"codex", `x={ y = "danger~u002dfull~u002daccess" }`},
		// The key web_search with the value live, only in a decoded TOML
		// value: the key alone is no danger.
		{"codex", `web_search="~u006cive"`},
	} {
		arg := esc(c.arg)
		if !strings.Contains(arg, `\u00`) {
			t.Fatalf("%q holds no escape", arg)
		}
		a, _ := Find(c.kind)
		if len(a.Dangers([]string{arg})) == 0 {
			t.Errorf("%s %q: not refused", c.kind, arg)
		}
	}
	// A rule that only a decoded value can match.
	a := Agent{DangerText: []string{"secret-mode"}}
	if arg := esc(`{"mode":"secret~u002dmode"}`); len(a.Dangers([]string{arg})) == 0 {
		t.Errorf("%q: not refused", arg)
	}
	if arg := esc(`mode="secret~u002dmode"`); len(a.Dangers([]string{arg})) == 0 {
		t.Errorf("%q: not refused", arg)
	}
}

// No agent's own commands, with its hooks and its inline mode, turn its
// approvals off, so a restore or a resume never refuses them.
func TestTheTableIsSafe(t *testing.T) {
	for _, a := range Table {
		for _, cmd := range [][]string{a.Launch, a.Resume, a.Continue} {
			argv := slices.Concat(cmd, a.Hooks, a.Inline.Args)
			if got := firstDanger(a, argv); got != "" {
				t.Errorf("%s: its own command %q has %q, which turns its approvals off", a.Kind, argv, got)
			}
		}
	}
}

// CheckID says why it refuses an ID.
func TestCheckID(t *testing.T) {
	claude, _ := Find("claude")
	if err := claude.CheckID("3f2b8c1e-9d4a-4e6b-8f7c-1a2b3c4d5e6f"); err != nil {
		t.Errorf("a UUID: %v", err)
	}
	if err := claude.CheckID("--yolo"); !errors.Is(err, ErrWrongIDForm) {
		t.Errorf("a flag: %v", err)
	}
	if err := (Agent{}).CheckID("x"); !errors.Is(err, ErrNoIDForm) {
		t.Errorf("no form: %v", err)
	}
	bad := Agent{IDPattern: "(["}
	for range 2 {
		if err := bad.CheckID("(["); err == nil || !strings.Contains(err.Error(), "does not compile") {
			t.Errorf("a form that does not compile: %v", err)
		}
	}
}

// onceRuns gives each run of TestCheckIDCompilesOnce its own pattern.
var onceRuns atomic.Int32

// Each form compiles once, even when many callers ask for it at the same
// time, the first time. Run it with -race. Each run uses a new pattern,
// so that -count=2 does not find the last run's compile in the cache.
func TestCheckIDCompilesOnce(t *testing.T) {
	var compiles atomic.Int32
	compile := compileIDForm
	run := onceRuns.Add(1)
	a, id := Agent{IDPattern: fmt.Sprintf("^once%d-[0-9]+$", run)}, fmt.Sprintf("once%d-1", run)
	idForms.Delete(a.IDPattern)
	t.Cleanup(func() {
		compileIDForm = compile
		idForms.Delete(a.IDPattern)
	})
	compileIDForm = func(p string) (*regexp.Regexp, error) {
		compiles.Add(1)
		return compile(p)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 64 {
		wg.Go(func() {
			<-start
			if err := a.CheckID(id); err != nil {
				t.Error(err)
			}
		})
	}
	close(start)
	wg.Wait()
	if n := compiles.Load(); n != 1 {
		t.Errorf("the form compiled %d times, want once", n)
	}
}

// A Codex session that an older helper started saved the resume command
// without its "--". It gets the current command. Any other command stays.
func TestResumeTemplate(t *testing.T) {
	codex, _ := Find("codex")
	claude, _ := Find("claude")
	for _, c := range []struct {
		agent        Agent
		stored, want []string
	}{
		{codex, []string{"codex", "--no-daemon", "resume", "{agent_session_id}"}, codex.Resume},
		{codex, codex.Resume, codex.Resume},
		{codex, []string{"codex", "resume", "{agent_session_id}"}, []string{"codex", "resume", "{agent_session_id}"}},
		{claude, claude.Resume, claude.Resume},
		{Agent{}, []string{"tool", "{agent_session_id}"}, []string{"tool", "{agent_session_id}"}},
	} {
		if got := c.agent.ResumeTemplate(c.stored); !slices.Equal(got, c.want) {
			t.Errorf("%s: ResumeTemplate(%q) = %q, want %q", c.agent.Kind, c.stored, got, c.want)
		}
	}
}
