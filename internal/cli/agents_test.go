package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func TestPrintAgentChecks(t *testing.T) {
	var b bytes.Buffer
	printAgentChecks(&b, "vps", []api.AgentCheck{
		{Kind: "claude", Title: "Claude Code", Installed: true, OnPath: true, Path: "/home/u/.local/bin/claude", Login: api.LoginYes, LoginHow: "`claude auth status` says so"},
		{Kind: "codex", Title: "Codex", Installed: true, OnPath: true, Path: "/home/u/.local/bin/codex", Login: api.LoginNo, LoginHow: "`codex login status` says not"},
		{Kind: "opencode", Title: "opencode", Installed: true, Path: "/home/u/.opencode/bin/opencode"},
		{Kind: "antigravity", Title: "Antigravity CLI", Installed: true, OnPath: true, Path: "/home/u/.local/bin/agy", Login: api.LoginUnknown},
		{Kind: "gemini", Title: "Gemini CLI"},
		{Kind: "aider", Title: "Aider", Installed: true, OnPath: true, Path: "/home/u/.local/bin/aider", Login: api.LoginNo},
	})
	out := b.String()
	for _, want := range []string{
		"✓ Claude Code      /home/u/.local/bin/claude, logged in (`claude auth status` says so)",
		"! Codex            /home/u/.local/bin/codex, not logged in",
		"Try: " + brand.Name + " new vps -- codex login --device-auth",
		"✕ opencode         /home/u/.opencode/bin/opencode is not on PATH in a login shell",
		`echo 'export PATH="/home/u/.opencode/bin:$PATH"' >> ~/.profile`,
		"Try: if it asks you to log in: " + brand.Name + " new vps --agent antigravity",
		"✕ Gemini CLI       not installed",
		"Try: " + brand.Name + " agents install gemini --host vps",
		"Try: pass its key from your laptop: " + brand.Name + " new vps --agent aider --secret ANTHROPIC_API_KEY",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestPickHost(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := pickHost(""); err == nil || !strings.Contains(err.Error(), "no hosts") {
		t.Fatalf("no hosts: %v", err)
	}
	if err := client.SaveHostsContext(context.Background(), []client.Host{{Name: "a", SSH: "a"}}); err != nil {
		t.Fatal(err)
	}
	if h, err := pickHost(""); err != nil || h.Name != "a" {
		t.Fatalf("one host: %v %v", h, err)
	}
	_ = client.SaveHostsContext(context.Background(), []client.Host{{Name: "a", SSH: "a"}, {Name: "b", SSH: "b"}})
	_, err := pickHost("")
	var ue *client.UserError
	if err == nil || !strings.Contains(err.Error(), "which host? You have a, b") {
		t.Fatalf("two hosts: %v", err)
	}
	if !errors.As(err, &ue) || ue.Hint != "Add --host <name>." {
		t.Fatalf("hint: %+v", ue)
	}
	if h, err := pickHost("b"); err != nil || h.Name != "b" {
		t.Fatalf("named host: %v %v", h, err)
	}
}

func TestInstallableAgentsHaveALoginCommand(t *testing.T) {
	for _, a := range agents.Installable() {
		if a.Setup.Login == "" && len(a.Setup.LoginEnv) == 0 {
			t.Errorf("%s: say how to log in, or which variable holds its key", a.Kind)
		}
	}
}
