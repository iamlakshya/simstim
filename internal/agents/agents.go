// Package agents is the product's knowledge of each agent CLI, kept as data.
//
// Nothing else in the code base branches on an agent's name. To support a new agent,
// add a row here.
package agents

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// Agent describes one agent CLI.
type Agent struct {
	Kind     string   `json:"kind"`
	Title    string   `json:"title"`
	Binaries []string `json:"binaries"`
	// Launch is the argv to start a new session. {session_uuid} is replaced.
	Launch []string `json:"launch"`
	// Resume and Continue are used to respawn after a host reboot (v2).
	// {agent_session_id} is replaced. Where the agent takes the ID as a
	// positional argument and accepts "--" before it, Resume has "--" there.
	// The hooks' and the inline mode's arguments then go before the "--".
	Resume   []string `json:"resume,omitempty"`
	Continue []string `json:"continue,omitempty"`
	// IDCapture says how we learn the agent's own session ID:
	// "choose" (we pass it), "hook", "scan" or "none".
	IDCapture string `json:"id_capture"`
	// Hooks are extra arguments that make the agent report its status and session
	// ID to the holder. {hook} is replaced by the command that forwards an event.
	Hooks []string `json:"hooks,omitempty"`
	// IDPattern is a regular expression for the agent's own session ID. An ID
	// enters a resume command only when it matches. The host checks it when a
	// hook reports it, and again before each resume, so it also checks an ID
	// that an older helper saved. An ID that the host chose itself (IDSource
	// "chosen") needs no match. Without a pattern, the host keeps no other ID.
	IDPattern string `json:"id_pattern,omitempty"`
	// DangerFlags are arguments that turn the agent's own approvals off. The
	// host refuses a command with one unless the user typed that argument:
	// the same argument, with the same values, in the session's own command.
	// "--flag" is a switch. "--flag=*" takes one value, any value.
	// "--flag=a|b" takes one value and counts only with one of these, and
	// "--flag!=a|b" with any value but these. "--flag=..." takes a list of
	// values, up to the next argument that starts with "-". A value is joined
	// with "=", joined to a short flag, or the next argument. The value "{}"
	// stands for a JSON object (danger.go).
	DangerFlags []string `json:"danger_flags,omitempty"`
	// DangerText is text that makes any argument dangerous, such as
	// "bypassPermissions" inside a --settings value. The host checks it as it
	// checks DangerFlags. It checks the argument as written, and the keys and
	// strings that it decodes to: a JSON object, or a key=value override
	// whose value is TOML. "key!=a|b" is that key with any value but these,
	// and "key=*" is that key with any value (danger.go).
	DangerText []string `json:"danger_text,omitempty"`
	// Support says how much the project promises for this agent.
	Support Support `json:"support"`
	// Inline is how to start the agent in its inline mode, where it draws in
	// the terminal's normal screen, so the terminal keeps its scrollback. An
	// agent that draws full screen (on the alternate screen) keeps its own
	// scrollback instead. Empty when the agent has no such mode.
	Inline Inline `json:"inline,omitzero"`
	// Memory is the least memory that the agent's own docs ask a machine to
	// have, in bytes, or 0 when they name none. It is a system requirement,
	// not a size for one process. The host doctor warns when a host has less.
	// In October 2026, the docs of opencode and Antigravity CLI named none.
	Memory int64 `json:"memory,omitempty"`
	// Setup is how to install the agent and log in (setup.go).
	Setup *Setup `json:"setup,omitempty"`
}

// GB is a gigabyte as agents' docs give memory: 10^9 bytes, a little less
// than a GiB. A machine of "4 GB" has 3.7 GiB.
const GB = 1_000_000_000

// Inline is an agent's switch to its inline mode: arguments, environment
// variables, or both.
type Inline struct {
	Args []string          `json:"args,omitempty"`
	Env  map[string]string `json:"env,omitempty"`
}

// Has is true when the agent has an inline mode.
func (i Inline) Has() bool { return len(i.Args) > 0 || len(i.Env) > 0 }

// Support is how much the project promises for an agent.
type Support string

// The support levels, from most to least.
const (
	// FirstClass agents are tested end to end on every release, report their
	// status through hooks, resume after a reboot, and can be installed with
	// agents install.
	FirstClass Support = "first-class"
	// Supported agents are in the table and tested when the test host has
	// them; their status comes from screen rules.
	Supported Support = "supported"
	// BestEffort agents are in the table, and contributors keep them working.
	BestEffort Support = "best-effort"
)

// uuidPattern is the form of a UUID in lower case. It is the IDPattern of
// an agent whose session IDs are UUIDs.
const uuidPattern = `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`

// claudeHooks makes Claude Code report its session ID, prompts, finished turns
// and notifications, through --settings (merged with the user's own settings).
const claudeHooks = `{"hooks":{` +
	`"SessionStart":[{"hooks":[{"type":"command","command":"{hook} session-start","timeout":5}]}],` +
	`"UserPromptSubmit":[{"hooks":[{"type":"command","command":"{hook} prompt","timeout":5}]}],` +
	`"Stop":[{"hooks":[{"type":"command","command":"{hook} stop","timeout":5}]}],` +
	`"Notification":[{"hooks":[{"type":"command","command":"{hook} notification","timeout":5}]}]}}`

// Table is the built-in agent table, checked against each agent's docs in
// October 2026. First-class agents come first: the launcher lists them in
// this order.
var Table = []Agent{
	{
		Kind: "claude", Title: "Claude Code", Binaries: []string{"claude"},
		Launch:    []string{"claude", "--session-id", "{session_uuid}"},
		Resume:    []string{"claude", "--resume", "{agent_session_id}"},
		Continue:  []string{"claude", "--continue"},
		IDCapture: "choose",
		Hooks:     []string{"--settings", claudeHooks},
		// --session-id "must be a valid UUID":
		// https://code.claude.com/docs/en/cli-reference.
		IDPattern: uuidPattern,
		// https://code.claude.com/docs/en/permission-modes: every mode but
		// default (alias manual) and plan changes what runs without a
		// prompt. The CLI reference: --allowedTools approves tools in
		// advance, --add-dir "grants file access" (both take a list), a
		// plugin can bring hooks, and --settings takes a path, which no
		// check can read.
		DangerFlags: []string{"--dangerously-skip-permissions", "--allow-dangerously-skip-permissions",
			"--permission-mode!=default|manual|plan", "--allowedTools=...", "--allowed-tools=...", "--add-dir=...",
			"--plugin-dir=*", "--plugin-url=*", "--settings!={}"},
		// In inline JSON (--settings, --agents): the mode, the allow list and
		// the extra folders (settings-reference), and the hooks that can
		// approve a tool call (hooks-guide). A subagent has its own
		// permissionMode (sub-agents). sandbox.autoAllowBashIfSandboxed
		// "Run sandboxed commands without a permission prompt", and is true
		// by default, so sandbox.enabled alone turns it on (settings-reference).
		DangerText: []string{"bypassPermissions", "defaultMode!=default|manual|plan", "permissionMode!=default|manual|plan",
			"permissions.allow", "additionalDirectories", "PermissionRequest", "PreToolUse",
			"autoAllowBashIfSandboxed!=false", "sandbox.enabled!=false"},
		Support: FirstClass,
		// https://code.claude.com/docs/en/fullscreen: forces the classic
		// renderer, which keeps the conversation in the terminal's scrollback.
		Inline: Inline{Env: map[string]string{"CLAUDE_CODE_DISABLE_ALTERNATE_SCREEN": "1"}},
		// "4 GB+ RAM": https://code.claude.com/docs/en/setup#system-requirements.
		Memory: 4 * GB,
		Setup: &Setup{
			Install: "curl -fsSL https://claude.ai/install.sh | bash",
			Source:  "https://code.claude.com/docs/en/setup",
			Paths:   []string{"~/.local/bin/claude"},
			// `claude auth login` and `claude auth status` (exit 0 when logged
			// in, 1 when not): https://code.claude.com/docs/en/cli-reference.
			// The credentials file and the variables:
			// https://code.claude.com/docs/en/authentication.
			Login:       "claude auth login",
			LoginStatus: []string{"claude", "auth", "status"},
			LoginFiles:  []string{"~/.claude/.credentials.json"},
			LoginEnv:    []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"},
		},
	},
	{
		Kind: "codex", Title: "Codex", Binaries: []string{"codex"},
		// --no-daemon keeps Codex's work inside the session's PTY and cgroup.
		Launch: []string{"codex", "--no-daemon"},
		// SESSION_ID is a plain positional argument for clap, so "--" ends
		// the options before it (ResumeCommand in codex-rs/cli/src/main.rs).
		Resume:    []string{"codex", "--no-daemon", "resume", "--", "{agent_session_id}"},
		Continue:  []string{"codex", "--no-daemon", "resume", "--last"},
		IDCapture: "hook",
		// Codex runs the notify program after each turn, with a JSON argument.
		Hooks: []string{"-c", `notify=["{hook_bin}","hook","codex-notify"]`},
		// "Codex-generated thread IDs are UUIDv7"
		// (codex-rs/protocol/src/thread_id.rs, tag rust-v0.167.0).
		IDPattern: uuidPattern,
		// codex-rs/utils/cli/src/shared_options.rs and approval_mode_cli_arg.rs,
		// and --search in tui/src/cli.rs ("no per-call approval"), at
		// rust-v0.167.0. --add-dir makes one more folder writable each time.
		// --enable FEATURE is -c features.FEATURE=true (FeatureToggles in
		// cli/src/main.rs). Of the known features, only web_search and
		// web_search_request give a key that the text rules below refuse.
		// --disable sets false, which they allow.
		DangerFlags: []string{"--dangerously-bypass-approvals-and-sandbox", "--yolo", "--dangerously-bypass-hook-trust",
			"--approve-for-me", "--not-so-yolo", "--profile=*", "-p=*", "-a=never", "--ask-for-approval=never",
			"--add-dir=*", "--search", "--enable=web_search|web_search_request"},
		// The keys of -c (config.schema.json at rust-v0.167.0), and the value
		// of -s danger-full-access. The flags' own -c forms: --search sets
		// web_search="live" (tui/src/startup_orchestration.rs), whose default
		// is "cached", and the legacy features.web_search_request=true means
		// "live" too (the config reference). Hook trust is kept in
		// hooks.state, and --add-dir matches sandbox_workspace_write.
		DangerText: []string{"danger-full-access", "approval_policy", "sandbox_mode", "approvals_reviewer",
			"default_permissions", "permissions=*", "permissions.", "profile=*", "sandbox_workspace_write",
			"web_search!=disabled|cached|false", "web_search_request!=false", "hooks.state"},
		Support: FirstClass,
		// codex-rs/tui/src/cli.rs in openai/codex: "Runs the TUI in inline
		// mode, preserving terminal scrollback history."
		Inline: Inline{Args: []string{"--no-alt-screen"}},
		// "4-GB minimum (8-GB recommended)":
		// https://github.com/openai/codex/blob/main/docs/install.md#system-requirements.
		Memory: 4 * GB,
		Setup: &Setup{
			// The installer puts codex in $CODEX_INSTALL_DIR, by default ~/.local/bin.
			Install: "curl -fsSL https://chatgpt.com/codex/install.sh | sh",
			Source:  "https://github.com/openai/codex#readme",
			Paths:   []string{"~/.local/bin/codex"},
			// Device-code login for hosts without a browser, and auth.json:
			// https://developers.openai.com/codex/auth. `codex login status`
			// exits 0 when logged in and 1 when not (codex-rs/cli/src/login.rs).
			Login:       "codex login --device-auth",
			LoginStatus: []string{"codex", "login", "status"},
			LoginFiles:  []string{"~/.codex/auth.json"},
		},
	},
	{
		Kind: "opencode", Title: "opencode", Binaries: []string{"opencode"},
		Launch:    []string{"opencode"},
		Resume:    []string{"opencode", "-s", "{agent_session_id}"},
		Continue:  []string{"opencode", "-c"},
		IDCapture: "scan",
		Support:   Supported,
		Setup: &Setup{
			Install: "curl -fsSL https://opencode.ai/install | bash",
			Source:  "https://opencode.ai/docs/",
			Paths:   []string{"~/.opencode/bin/opencode"},
			// `opencode auth login` keeps keys in auth.json: https://opencode.ai/docs/cli/.
			Login:      "opencode auth login",
			LoginFiles: []string{"~/.local/share/opencode/auth.json"},
		},
	},
	{
		// Antigravity CLI replaced Gemini CLI for Google's consumer plans on
		// 18 June 2026 (https://www.antigravity.google/docs/cli/commands/resume).
		Kind: "antigravity", Title: "Antigravity CLI", Binaries: []string{"agy"},
		Launch:    []string{"agy"},
		Resume:    []string{"agy", "--conversation", "{agent_session_id}"},
		Continue:  []string{"agy", "--continue"},
		IDCapture: "none",
		Support:   Supported,
		Setup: &Setup{
			// The first start walks through the sign-in. The docs name no
			// status command or credentials file, so the login is unknown.
			Install: "curl -fsSL https://antigravity.google/cli/install.sh | bash",
			Source:  "https://www.antigravity.google/docs/getting-started?tab=cli",
			Paths:   []string{"~/.local/bin/agy"},
			Login:   "agy",
		},
	},
	{
		Kind: "gemini", Title: "Gemini CLI", Binaries: []string{"gemini"},
		Launch:    []string{"gemini", "--session-id", "{session_uuid}"},
		Resume:    []string{"gemini", "--resume", "{agent_session_id}"},
		Continue:  []string{"gemini", "--resume"},
		IDCapture: "choose",
		Support:   BestEffort, // consumer plans moved to Antigravity CLI
		Setup: &Setup{
			Install: "npm install -g @google/gemini-cli",
			Source:  "https://github.com/google-gemini/gemini-cli#readme",
			// The first start offers "Sign in with Google", kept in
			// ~/.gemini/oauth_creds.json (packages/core/src/config/storage.ts);
			// the variables: https://www.geminicli.com/docs/get-started/authentication.
			Login:      "gemini",
			LoginFiles: []string{"~/.gemini/oauth_creds.json"},
			LoginEnv:   []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"},
		},
	},
	{
		Kind: "amp", Title: "Amp", Binaries: []string{"amp"},
		Launch:    []string{"amp"},
		Resume:    []string{"amp", "threads", "continue", "{agent_session_id}"},
		Continue:  []string{"amp", "threads", "continue", "--last"},
		IDCapture: "scan",
		Support:   BestEffort,
		Setup: &Setup{
			// `amp login` and AMP_API_KEY: https://ampcode.com/docs/cli.
			Install:  "curl -fsSL https://ampcode.com/install.sh | bash",
			Source:   "https://ampcode.com/docs/cli",
			Paths:    []string{"~/.local/bin/amp", "~/.amp/bin/amp"},
			Login:    "amp login",
			LoginEnv: []string{"AMP_API_KEY"},
		},
	},
	{
		Kind: "cursor", Title: "Cursor CLI", Binaries: []string{"agent", "cursor-agent"},
		Launch:    []string{"agent"},
		Resume:    []string{"agent", "--resume", "{agent_session_id}"},
		Continue:  []string{"agent", "--continue"},
		IDCapture: "scan",
		Support:   BestEffort,
		Setup: &Setup{
			// `agent login` and CURSOR_API_KEY:
			// https://cursor.com/docs/cli/reference/authentication.
			Install:  "curl https://cursor.com/install -fsS | bash",
			Source:   "https://cursor.com/docs/cli/installation",
			Paths:    []string{"~/.local/bin/agent", "~/.local/bin/cursor-agent"},
			Login:    "agent login",
			LoginEnv: []string{"CURSOR_API_KEY"},
		},
	},
	{
		Kind: "aider", Title: "Aider", Binaries: []string{"aider"},
		Launch:    []string{"aider"},
		Continue:  []string{"aider", "--restore-chat-history"},
		IDCapture: "none",
		Support:   BestEffort,
		Setup: &Setup{
			// Aider has no login of its own: it uses the model provider's API key.
			Install:  "curl -LsSf https://aider.chat/install.sh | sh",
			Source:   "https://aider.chat/docs/install.html",
			Paths:    []string{"~/.local/bin/aider"},
			LoginEnv: []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"},
		},
	},
	{
		Kind: "goose", Title: "Goose", Binaries: []string{"goose"},
		Launch:    []string{"goose", "session"},
		Continue:  []string{"goose", "session", "-r"},
		IDCapture: "none",
		Support:   BestEffort,
		Setup: &Setup{
			// `goose configure` sets up a provider and its key.
			Install: "curl -fsSL https://github.com/aaif-goose/goose/releases/download/stable/download_cli.sh | bash",
			Source:  "https://goose-docs.ai/docs/getting-started/installation",
			Paths:   []string{"~/.local/bin/goose"},
			Login:   "goose configure",
		},
	},
	{
		Kind: "shell", Title: "Shell", Binaries: []string{"$SHELL"},
		Launch:    []string{"$SHELL", "-l"},
		Continue:  []string{"$SHELL", "-l"},
		IDCapture: "none",
		Support:   Supported, // and through it, any program
	},
}

// Find returns the agent with the given kind.
func Find(kind string) (Agent, bool) {
	for _, a := range Table {
		if a.Kind == kind {
			return a, true
		}
	}
	return Agent{}, false
}

// The reasons that CheckID gives for an ID that it refuses.
var (
	// ErrNoIDForm is for an agent whose row has no IDPattern.
	ErrNoIDForm = errors.New("the agent table gives no form for this agent's session IDs")
	// ErrWrongIDForm is for an ID that does not match the agent's IDPattern.
	ErrWrongIDForm = errors.New("it does not have the form of the agent's session IDs")
)

// idForms holds, for each IDPattern, a function that compiles it the
// first time it runs, and then returns the same result.
var idForms sync.Map // string -> func() (*regexp.Regexp, error)

// compileIDForm compiles an IDPattern to match a whole ID. A test counts
// its calls.
var compileIDForm = func(pattern string) (*regexp.Regexp, error) {
	re, err := regexp.Compile(`^(?:` + pattern + `)$`)
	if err != nil {
		return nil, fmt.Errorf("the agent table's form for this agent's session IDs does not compile: %w", err)
	}
	return re, nil
}

// idForm returns pattern compiled. Callers that ask at the same time, the
// first time, share one compile: the function is stored before it runs.
func idForm(pattern string) (*regexp.Regexp, error) {
	v, ok := idForms.Load(pattern)
	if !ok {
		v, _ = idForms.LoadOrStore(pattern, sync.OnceValues(func() (*regexp.Regexp, error) { return compileIDForm(pattern) }))
	}
	compile, _ := v.(func() (*regexp.Regexp, error))
	return compile()
}

// CheckID reports whether id, as a whole, has the form of the agent's own
// session IDs, IDPattern. It returns nil when it does. An agent without a
// pattern has no known form, so no ID passes (ErrNoIDForm). A pattern that
// does not compile passes no ID either, and the error says so.
func (a Agent) CheckID(id string) error {
	if a.IDPattern == "" {
		return ErrNoIDForm
	}
	re, err := idForm(a.IDPattern)
	switch {
	case err != nil:
		return err
	case !re.MatchString(id):
		return ErrWrongIDForm
	}
	return nil
}

// ValidID reports whether id has the form of the agent's own session IDs
// (CheckID).
func (a Agent) ValidID(id string) bool { return a.CheckID(id) == nil }

// ResumeTemplate returns the resume command to expand for a session that
// saved stored, a copy of Resume from when it started. An older helper
// saved the same command without its "--". That one gets the current
// command, so that the ID stays a positional argument. Any other command
// stays as it was saved.
func (a Agent) ResumeTemplate(stored []string) []string {
	older := slices.DeleteFunc(slices.Clone(a.Resume), func(s string) bool { return s == "--" })
	if len(older) < len(a.Resume) && slices.Equal(stored, older) {
		return slices.Clone(a.Resume)
	}
	return stored
}

// Expand replaces placeholders in argv.
func Expand(argv []string, vars map[string]string) []string {
	out := make([]string, len(argv))
	for i, a := range argv {
		for k, v := range vars {
			a = strings.ReplaceAll(a, "{"+k+"}", v)
		}
		out[i] = a
	}
	return out
}
