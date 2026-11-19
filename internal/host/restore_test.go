package host

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/proto"
)

// Each kind of session comes back with its own command, in this order: the
// agent's resume command when the session recorded the agent's ID, the
// user's command for a session that started with one, the agent's continue
// command, and last the command of the last start. The shell agent has only
// a continue command, a login shell.
func TestResumeArgv(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")
	cont := []string{"$SHELL", "-l"}
	own := []string{"/bin/sh", "-c", "exec htop"}
	resume := []string{"agent", "--resume", "{agent_session_id}"}
	agentCont := []string{"agent", "--continue"}
	for _, c := range []struct {
		name  string
		agent AgentInfo
		argv  []string
		why   string
	}{
		{"the shell agent", AgentInfo{Kind: "shell", Argv: []string{"/bin/bash", "-l"}, ContinueArgv: cont},
			[]string{"/bin/zsh", "-l"}, "continued the latest conversation in its folder"},
		{"an agent with its session ID", AgentInfo{Kind: "agent", Argv: []string{"agent", "--hook"}, AgentSessionID: "s1", IDSource: "chosen",
			ResumeArgv: resume, ContinueArgv: agentCont, Hooks: []string{"--hook"}},
			[]string{"agent", "--resume", "s1", "--hook"}, "resumed its own session s1"},
		{"an agent without it", AgentInfo{Kind: "agent", Argv: []string{"agent", "--hook", "--inline"},
			ResumeArgv: resume, ContinueArgv: agentCont, Hooks: []string{"--hook"}, Inline: []string{"--inline"}},
			[]string{"agent", "--continue", "--hook", "--inline"}, "continued the latest conversation in its folder"},
		// An agent whose table row gives no form for its IDs keeps none from
		// a hook. An older helper kept one, which enters no command.
		{"an agent with an ID from a hook", AgentInfo{Kind: "agent", Argv: []string{"agent"}, AgentSessionID: "s1", IDSource: "hook",
			ResumeArgv: resume, ContinueArgv: agentCont},
			[]string{"agent", "--continue"}, "continued the latest conversation in its folder"},
		{"a command with the shell agent", AgentInfo{Kind: "shell", Argv: own, Command: own, ContinueArgv: cont},
			own, "started its own command again"},
		{"a command after a start that changed Argv", AgentInfo{Kind: "shell", Argv: []string{"/bin/zsh", "-l"}, Command: own, ContinueArgv: cont},
			own, "started its own command again"},
		// A resume by ID keeps the conversation, without the command's own
		// arguments.
		{"a command with an agent and its session ID", AgentInfo{Kind: "agent", Argv: own, Command: own, AgentSessionID: "s1", IDSource: "chosen",
			ResumeArgv: resume, ContinueArgv: agentCont},
			[]string{"agent", "--resume", "s1"}, "resumed its own session s1"},
		// New records no ID that the command did not pass to the agent.
		{"a command with an agent that never got its ID", AgentInfo{Kind: "agent", Argv: own, Command: own,
			ResumeArgv: resume, ContinueArgv: agentCont},
			own, "started its own command again"},
		{"a command with an agent that has no resume command", AgentInfo{Kind: "agent", Argv: own, Command: own, AgentSessionID: "s1",
			IDSource: "chosen", ContinueArgv: agentCont},
			own, "started its own command again"},
		{"a command in an older manifest", AgentInfo{Kind: "custom", Argv: own}, own, "started fresh"},
	} {
		m := &Manifest{Agent: c.agent}
		argv, why, err := resumeArgv(m)
		if err != nil || !slices.Equal(argv, c.argv) || why != c.why {
			t.Errorf("%s: %q (%s, %v), want %q (%s)", c.name, argv, why, err, c.argv, c.why)
			continue
		}
		// The manifest keeps the command for the next start, whatever
		// happens to this one's.
		argv[0] = "changed"
		if len(c.agent.Command) > 0 && !slices.Equal(m.Agent.Command, []string{"/bin/sh", "-c", "exec htop"}) {
			t.Errorf("%s: the command became %q", c.name, m.Agent.Command)
		}
	}
}

// firstClass returns the record of a new session of a first-class agent,
// as New makes it: with the agent's commands, its hooks and its inline mode.
func firstClass(t *testing.T, kind string) AgentInfo {
	t.Helper()
	a, ok := agents.Find(kind)
	if !ok {
		t.Fatalf("no agent %s", kind)
	}
	return AgentInfo{Kind: kind, Argv: a.Launch, ResumeArgv: a.Resume, ContinueArgv: a.Continue,
		Hooks: hookArgs(a.Hooks), Inline: a.Inline.Args}
}

// A first-class agent resumes by its own session ID only when the ID has
// the agent's form. Any other ID, such as one that an older helper kept
// from a hook, enters no command: the agent continues the latest
// conversation instead. Codex takes the ID after "--", and its hooks and
// its inline mode before it.
func TestResumeArgvChecksTheID(t *testing.T) {
	const uuid4, uuid7 = "3f2b8c1e-9d4a-4e6b-8f7c-1a2b3c4d5e6f", "0199a3f1-7c2e-7d4b-9a5e-3f1c2b4d5e6f"
	resumed, continued := "resumed its own session ", "continued the latest conversation in its folder"
	for _, c := range []struct {
		kind, id, source string
		want             func(a AgentInfo) []string
		why              string
	}{
		{"claude", uuid4, "chosen", func(a AgentInfo) []string {
			return slices.Concat([]string{"claude", "--resume", uuid4}, a.Hooks, a.Inline)
		}, resumed + uuid4},
		{"claude", uuid4, "hook", func(a AgentInfo) []string {
			return slices.Concat([]string{"claude", "--resume", uuid4}, a.Hooks, a.Inline)
		}, resumed + uuid4},
		{"codex", uuid7, "hook", func(a AgentInfo) []string {
			return slices.Concat([]string{"codex", "--no-daemon", "resume"}, a.Hooks, a.Inline, []string{"--", uuid7})
		}, resumed + uuid7},
	} {
		a := firstClass(t, c.kind)
		a.AgentSessionID, a.IDSource = c.id, c.source
		argv, why, err := resumeArgv(&Manifest{Agent: a})
		if err != nil || !slices.Equal(argv, c.want(a)) || why != c.why {
			t.Errorf("%s with ID %q: %q (%s, %v), want %q", c.kind, c.id, argv, why, err, c.want(a))
		}
	}
	// A Codex session that an older helper started saved its resume command
	// without "--". It resumes with the current one.
	a := firstClass(t, "codex")
	a.ResumeArgv = []string{"codex", "--no-daemon", "resume", "{agent_session_id}"}
	a.AgentSessionID, a.IDSource = uuid7, "hook"
	argv, _, err := resumeArgv(&Manifest{Agent: a})
	if want := slices.Concat([]string{"codex", "--no-daemon", "resume"}, a.Hooks, a.Inline, []string{"--", uuid7}); err != nil || !slices.Equal(argv, want) {
		t.Errorf("codex with an older resume command: %q (%v), want %q", argv, err, want)
	}
	// Gemini CLI's row has no form, but the host chose the ID.
	gemini, _ := agents.Find("gemini")
	g := AgentInfo{Kind: "gemini", ResumeArgv: gemini.Resume, ContinueArgv: gemini.Continue, AgentSessionID: uuid4, IDSource: "chosen"}
	if argv, why, err := resumeArgv(&Manifest{Agent: g}); err != nil || !slices.Equal(argv, []string{"gemini", "--resume", uuid4}) || why != resumed+uuid4 {
		t.Errorf("gemini with a chosen ID: %q (%s, %v)", argv, why, err)
	}
	for _, kind := range []string{"claude", "codex"} {
		for _, id := range []string{
			"--settings=/tmp/x.json", "--dangerously-skip-permissions", "--permission-mode=bypassPermissions", "--yolo", "-c", "-p",
			uuid4 + " --yolo", uuid4 + "\n", " " + uuid4, strings.ToUpper(uuid4), "abc-123", "../" + uuid4,
		} {
			a := firstClass(t, kind)
			a.AgentSessionID, a.IDSource = id, "hook"
			argv, why, err := resumeArgv(&Manifest{Agent: a})
			want := slices.Concat(a.ContinueArgv, a.Hooks, a.Inline)
			if err != nil || !slices.Equal(argv, want) || why != continued {
				t.Errorf("%s with ID %q: %q (%s, %v), want %q", kind, id, argv, why, err, want)
			}
		}
	}
}

// A command that turns the agent's own approvals off is refused, unless the
// user typed that argument in the session's own command. The agents' own
// commands, hooks and inline modes never hold one.
func TestResumeArgvRefusesDanger(t *testing.T) {
	const id = "3f2b8c1e-9d4a-4e6b-8f7c-1a2b3c4d5e6f"
	bypass := `{"permissions":{"defaultMode":"bypassPermissions"}}`
	escaped := func(s string) string { return strings.ReplaceAll(s, "~", `\`) }
	with := func(kind string, f func(a *AgentInfo)) AgentInfo {
		a := firstClass(t, kind)
		f(&a)
		return a
	}
	for _, c := range []struct {
		name  string
		agent AgentInfo
		bad   string // the argument that is refused, or "" when the command runs
	}{
		{"claude, resumed", with("claude", func(a *AgentInfo) { a.AgentSessionID, a.IDSource = id, "chosen" }), ""},
		{"claude, continued", firstClass(t, "claude"), ""},
		{"codex, resumed", with("codex", func(a *AgentInfo) { a.AgentSessionID, a.IDSource = id, "hook" }), ""},
		{"codex, continued", firstClass(t, "codex"), ""},

		{"claude's resume command in bypass mode", with("claude", func(a *AgentInfo) {
			a.AgentSessionID, a.IDSource = id, "chosen"
			a.ResumeArgv = slices.Concat(a.ResumeArgv, []string{"--permission-mode", "bypassPermissions"})
		}), "--permission-mode bypassPermissions"},
		{"claude's resume command in auto mode", with("claude", func(a *AgentInfo) {
			a.AgentSessionID, a.IDSource = id, "chosen"
			a.ResumeArgv = slices.Concat(a.ResumeArgv, []string{"--permission-mode", "auto"})
		}), "--permission-mode auto"},
		{"claude's settings in bypass mode", with("claude", func(a *AgentInfo) { a.Hooks = []string{"--settings", bypass} }), bypass},
		// JSON and TOML escapes do not hide a danger: only the decoded
		// argument holds it ("~" is a backslash).
		{"claude's settings in bypass mode, escaped", with("claude", func(a *AgentInfo) {
			a.Hooks = []string{"--settings", escaped(`{"permissions":{"defaultMode":"~u0062ypassPermissions"}}`)}
		}), escaped(`{"permissions":{"defaultMode":"~u0062ypassPermissions"}}`)},
		{"claude's settings from a file", with("claude", func(a *AgentInfo) { a.Hooks = []string{"--settings", "/tmp/x.json"} }),
			"--settings /tmp/x.json"},
		{"codex's sandbox, escaped in a value", with("codex", func(a *AgentInfo) {
			a.ContinueArgv = slices.Concat(a.ContinueArgv, []string{"-c", escaped(`x="danger~u002dfull~u002daccess"`)})
		}), escaped(`x="danger~u002dfull~u002daccess"`)},
		{"codex's web search, escaped in a value", with("codex", func(a *AgentInfo) {
			a.ContinueArgv = slices.Concat(a.ContinueArgv, []string{"-c", escaped(`web_search="~u006cive"`)})
		}), escaped(`web_search="~u006cive"`)},
		// The -c form of --search.
		{"codex's live web search", with("codex", func(a *AgentInfo) {
			a.ContinueArgv = slices.Concat(a.ContinueArgv, []string{"-c", `web_search="live"`})
		}), `web_search="live"`},
		{"codex's live web search, typed", with("codex", func(a *AgentInfo) {
			a.Command, a.Hooks = []string{"codex", "-c", `web_search="live"`}, nil
		}), ""},
		// The feature toggle of live search, in both forms.
		{"codex's live search feature", with("codex", func(a *AgentInfo) {
			a.ContinueArgv = slices.Concat(a.ContinueArgv, []string{"--enable", "web_search_request"})
		}), "--enable web_search_request"},
		{"codex's live search feature, joined", with("codex", func(a *AgentInfo) {
			a.ContinueArgv = slices.Concat(a.ContinueArgv, []string{"--enable=web_search_request"})
		}), "--enable=web_search_request"},
		{"codex's live search feature, typed", with("codex", func(a *AgentInfo) {
			a.Command, a.Hooks = []string{"codex", "--enable", "web_search_request"}, nil
		}), ""},
		// Claude Code's sandbox runs Bash without a prompt by default.
		{"claude's sandbox that allows Bash", with("claude", func(a *AgentInfo) {
			a.Hooks = []string{"--settings", `{"sandbox":{"enabled":true,"autoAllowBashIfSandboxed":true}}`}
		}), `{"sandbox":{"enabled":true,"autoAllowBashIfSandboxed":true}}`},
		{"claude's sandbox that allows Bash, escaped", with("claude", func(a *AgentInfo) {
			a.Hooks = []string{"--settings", escaped(`{"sandbox":{"~u0065nabled":true}}`)}
		}), escaped(`{"sandbox":{"~u0065nabled":true}}`)},
		{"claude's sandbox that allows Bash, typed", with("claude", func(a *AgentInfo) {
			a.Command, a.Hooks = []string{"claude", "--settings", `{"sandbox":{"enabled":true}}`}, nil
		}), ""},
		// Claude Code's extra folders.
		{"claude's extra folder", with("claude", func(a *AgentInfo) { a.Hooks = []string{"--add-dir", "/"} }), "--add-dir /"},
		{"claude's extra folders in its settings", with("claude", func(a *AgentInfo) {
			a.Hooks = []string{"--settings", `{"permissions":{"additionalDirectories":["/"]}}`}
		}), `{"permissions":{"additionalDirectories":["/"]}}`},
		// A flag's values count: one that the user typed allows only the
		// same values.
		{"claude's tools, other than typed", with("claude", func(a *AgentInfo) {
			a.Command, a.Hooks = []string{"claude", "--allowedTools", "Read"}, []string{"--allowedTools", "Bash"}
		}), "--allowedTools Bash"},
		{"claude's tools, more than typed", with("claude", func(a *AgentInfo) {
			a.Command, a.Hooks = []string{"claude", "--allowedTools", "Read"}, []string{"--allowedTools", "Read", "Bash"}
		}), "--allowedTools Read Bash"},
		{"claude's tools, as typed", with("claude", func(a *AgentInfo) {
			a.Command, a.Hooks = []string{"claude", "--allowedTools", "Read"}, []string{"--allowedTools", "Read"}
		}), ""},
		{"codex's extra folder, other than typed", with("codex", func(a *AgentInfo) {
			a.Command, a.Hooks = []string{"codex", "--add-dir", "/work"}, []string{"--add-dir", "/"}
		}), "--add-dir /"},
		{"codex's extra folder, as typed", with("codex", func(a *AgentInfo) {
			a.Command, a.Hooks = []string{"codex", "--add-dir", "/work"}, []string{"--add-dir", "/work"}
		}), ""},
		{"codex's profile, other than typed", with("codex", func(a *AgentInfo) {
			a.Command, a.Hooks = []string{"codex", "--profile", "safe"}, []string{"--profile", "unsafe"}
		}), "--profile unsafe"},
		{"codex's profile, as typed", with("codex", func(a *AgentInfo) {
			a.Command, a.Hooks = []string{"codex", "--profile", "safe"}, []string{"--profile", "safe"}
		}), ""},
		{"codex's approvals off", with("codex", func(a *AgentInfo) {
			a.ContinueArgv = slices.Concat(a.ContinueArgv, []string{"-a", "never"})
		}), "-a never"},
		{"claude's continue command that skips permissions", with("claude", func(a *AgentInfo) {
			a.ContinueArgv = slices.Concat(a.ContinueArgv, []string{"--dangerously-skip-permissions"})
		}), "--dangerously-skip-permissions"},
		{"codex's continue command with a sandbox mode", with("codex", func(a *AgentInfo) {
			a.ContinueArgv = slices.Concat(a.ContinueArgv, []string{"-c", `sandbox_mode="danger-full-access"`})
		}), `sandbox_mode="danger-full-access"`},
		{"codex's resume command with --yolo", with("codex", func(a *AgentInfo) {
			a.AgentSessionID, a.IDSource = id, "hook"
			a.ResumeArgv = []string{"codex", "--yolo", "resume", "--", "{agent_session_id}"}
		}), "--yolo"},
		{"codex's profile", with("codex", func(a *AgentInfo) { a.Inline = []string{"-pwork"} }), "-pwork"},

		// The user typed these, and the session's own command runs again.
		{"claude in bypass mode, typed", with("claude", func(a *AgentInfo) {
			a.Command = []string{"claude", "--permission-mode", "bypassPermissions"}
			a.Hooks = nil
		}), ""},
		{"claude's settings in bypass mode, typed", with("claude", func(a *AgentInfo) {
			a.Command = []string{"claude", "--settings", bypass}
			a.Hooks = nil
		}), ""},
		{"codex with a sandbox mode, typed", with("codex", func(a *AgentInfo) {
			a.Command = []string{"codex", "-c", `sandbox_mode="danger-full-access"`}
			a.Hooks = nil
		}), ""},
		// A resume by ID drops the typed flag, as it drops the command's
		// other arguments.
		{"claude resumed after a typed flag", with("claude", func(a *AgentInfo) {
			a.Command = []string{"claude", "--dangerously-skip-permissions", "--session-id", id}
			a.AgentSessionID, a.IDSource, a.Hooks = id, "chosen", nil
		}), ""},
		{"claude with a typed command and an untyped danger flag", with("claude", func(a *AgentInfo) {
			a.Command = []string{"claude", "--model", "opus"}
			a.Hooks = []string{"--dangerously-skip-permissions"}
		}), "--dangerously-skip-permissions"},
		// The rule is for each argument: one in the agent's resume command
		// or hooks passes when the session's own command holds it too, with
		// the same value.
		{"claude's resume command with a mode that the user typed", with("claude", func(a *AgentInfo) {
			a.Command = []string{"claude", "--permission-mode", "auto", "--session-id", id}
			a.AgentSessionID, a.IDSource, a.Hooks = id, "chosen", []string{"--dangerously-skip-permissions"}
			a.ResumeArgv = slices.Concat(a.ResumeArgv, []string{"--permission-mode", "auto"})
		}), "--dangerously-skip-permissions"},
		{"claude's resume command and hooks with flags that the user typed", with("claude", func(a *AgentInfo) {
			a.Command = []string{"claude", "--permission-mode", "auto", "--dangerously-skip-permissions", "--session-id", id}
			a.AgentSessionID, a.IDSource, a.Hooks = id, "chosen", []string{"--dangerously-skip-permissions"}
			a.ResumeArgv = slices.Concat(a.ResumeArgv, []string{"--permission-mode", "auto"})
		}), ""},
		{"claude's resume command with another mode than the user typed", with("claude", func(a *AgentInfo) {
			a.Command = []string{"claude", "--permission-mode", "acceptEdits", "--session-id", id}
			a.AgentSessionID, a.IDSource, a.Hooks = id, "chosen", nil
			a.ResumeArgv = slices.Concat(a.ResumeArgv, []string{"--permission-mode", "auto"})
		}), "--permission-mode auto"},
		// A program that is not in the agent table has no such flags.
		{"a command of no agent", AgentInfo{Kind: "custom", Argv: []string{"tool", "--yolo"}}, ""},
	} {
		argv, _, err := resumeArgv(&Manifest{Name: "app", Agent: c.agent})
		if c.bad == "" {
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
			}
			continue
		}
		var pe *proto.Error
		if !errors.As(err, &pe) || pe.Code != dangerRefused || pe.Hint == "" || argv != nil ||
			!strings.Contains(pe.Message, "app did not start again") || !strings.Contains(pe.Message, strconv.Quote(clip(c.bad, 60))) {
			t.Errorf("%s: %q, %v, want the %s error for %q, with a hint", c.name, argv, err, dangerRefused, c.bad)
		}
	}
}

// The extra arguments go before a "--" that ends the options, else at the
// end, and the command given stays as it was.
func TestWithOptions(t *testing.T) {
	for _, c := range []struct {
		argv, want []string
	}{
		{[]string{"a", "x"}, []string{"a", "x", "-h", "-i"}},
		{[]string{"a", "--", "x"}, []string{"a", "-h", "-i", "--", "x"}},
		{[]string{"a", "--", "x", "--", "y"}, []string{"a", "-h", "-i", "--", "x", "--", "y"}},
		{[]string{"--"}, []string{"-h", "-i", "--"}},
	} {
		in := slices.Clone(c.argv)
		if got := withOptions(in, []string{"-h"}, nil, []string{"-i"}); !slices.Equal(got, c.want) || !slices.Equal(in, c.argv) {
			t.Errorf("withOptions(%q) = %q, want %q", c.argv, got, c.want)
		}
	}
}
