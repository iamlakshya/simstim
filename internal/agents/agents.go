// Package agents is the product's knowledge of each agent CLI, kept as data.
//
// Nothing else in the code base branches on an agent's name. To support a new agent,
// add a row here.
package agents

import "strings"

// Agent describes one agent CLI.
type Agent struct {
	Kind     string   `json:"kind"`
	Title    string   `json:"title"`
	Binaries []string `json:"binaries"`
	// Launch is the argv to start a new session. {session_uuid} is replaced.
	Launch []string `json:"launch"`
	// Resume and Continue are used to respawn after a host reboot (v2).
	// {agent_session_id} is replaced.
	Resume   []string `json:"resume,omitempty"`
	Continue []string `json:"continue,omitempty"`
	// IDCapture says how we learn the agent's own session ID:
	// "choose" (we pass it), "hook", "scan" or "none".
	IDCapture string `json:"id_capture"`
	// Hooks are extra arguments that make the agent report its status and session
	// ID to the holder. {hook} is replaced by the command that forwards an event.
	Hooks []string `json:"hooks,omitempty"`
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
		Support:   FirstClass,
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
		Launch:    []string{"codex", "--no-daemon"},
		Resume:    []string{"codex", "--no-daemon", "resume", "{agent_session_id}"},
		Continue:  []string{"codex", "--no-daemon", "resume", "--last"},
		IDCapture: "hook",
		// Codex runs the notify program after each turn, with a JSON argument.
		Hooks:   []string{"-c", `notify=["{hook_bin}","hook","codex-notify"]`},
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
