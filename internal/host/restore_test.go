package host

import (
	"slices"
	"testing"
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
		{"an agent with its session ID", AgentInfo{Kind: "agent", Argv: []string{"agent", "--hook"}, AgentSessionID: "s1",
			ResumeArgv: resume, ContinueArgv: agentCont, Hooks: []string{"--hook"}},
			[]string{"agent", "--resume", "s1", "--hook"}, "resumed its own session s1"},
		{"an agent without it", AgentInfo{Kind: "agent", Argv: []string{"agent", "--hook", "--inline"},
			ResumeArgv: resume, ContinueArgv: agentCont, Hooks: []string{"--hook"}, Inline: []string{"--inline"}},
			[]string{"agent", "--continue", "--hook", "--inline"}, "continued the latest conversation in its folder"},
		{"a command with the shell agent", AgentInfo{Kind: "shell", Argv: own, Command: own, ContinueArgv: cont},
			own, "started its own command again"},
		{"a command after a start that changed Argv", AgentInfo{Kind: "shell", Argv: []string{"/bin/zsh", "-l"}, Command: own, ContinueArgv: cont},
			own, "started its own command again"},
		// A resume by ID keeps the conversation, without the command's own
		// arguments.
		{"a command with an agent and its session ID", AgentInfo{Kind: "agent", Argv: own, Command: own, AgentSessionID: "s1",
			ResumeArgv: resume, ContinueArgv: agentCont},
			[]string{"agent", "--resume", "s1"}, "resumed its own session s1"},
		// New records no ID that the command did not pass to the agent.
		{"a command with an agent that never got its ID", AgentInfo{Kind: "agent", Argv: own, Command: own,
			ResumeArgv: resume, ContinueArgv: agentCont},
			own, "started its own command again"},
		{"a command with an agent that has no resume command", AgentInfo{Kind: "agent", Argv: own, Command: own, AgentSessionID: "s1",
			ContinueArgv: agentCont},
			own, "started its own command again"},
		{"a command in an older manifest", AgentInfo{Kind: "custom", Argv: own}, own, "started fresh"},
	} {
		m := &Manifest{Agent: c.agent}
		argv, why := resumeArgv(m)
		if !slices.Equal(argv, c.argv) || why != c.why {
			t.Errorf("%s: %q (%s), want %q (%s)", c.name, argv, why, c.argv, c.why)
		}
		// The manifest keeps the command for the next start, whatever
		// happens to this one's.
		argv[0] = "changed"
		if len(c.agent.Command) > 0 && !slices.Equal(m.Agent.Command, []string{"/bin/sh", "-c", "exec htop"}) {
			t.Errorf("%s: the command became %q", c.name, m.Agent.Command)
		}
	}
}
