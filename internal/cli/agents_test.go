package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/onboard"
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
		"Try: " + brand.Name + " agents login codex vps",
		"✕ opencode         /home/u/.opencode/bin/opencode is not on PATH in a login shell",
		`echo 'export PATH="/home/u/.opencode/bin:$PATH"' >> ~/.profile`,
		"Try: if it asks you to log in: " + brand.Name + " agents login antigravity vps",
		"✕ Gemini CLI       not installed",
		"Try: " + brand.Name + " agents install gemini vps",
		"Try: pass its key from your laptop: " + brand.Name + " new vps --agent aider --secret ANTHROPIC_API_KEY",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestPrintAgentChecksCleansTheHostsText(t *testing.T) {
	var b bytes.Buffer
	printAgentChecks(&b, "vps", []api.AgentCheck{{
		Kind: "codex", Title: "Codex\x1b]52;c;aGk=\x07", Installed: true, OnPath: true,
		Path: "/p\x1b[2J/codex", Login: api.LoginNo, LoginHow: "says\u202e not\n",
	}})
	if out := b.String(); strings.ContainsAny(out, "\x1b\x07\u202e") || !strings.Contains(out, "! Codex]52;c;aGk=  /p[2J/codex, not logged in (says not )") {
		t.Errorf("printed %q", out)
	}
	b.Reset()
	printLine(&b, onboard.Line{Mark: "!", Title: "Codex\x1b[2J", State: "s\x07", Try: "its login still runs: attach box/a\x1b]52;c;aGk=\x07"})
	if out := b.String(); strings.ContainsAny(out, "\x1b\x07") || !strings.Contains(out, "    Try: its login still runs: attach box/a]52;c;aGk=\n") {
		t.Errorf("printLine printed %q", out)
	}
}

func TestPickHost(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := pickHost(""); err == nil || !strings.Contains(err.Error(), "no hosts") {
		t.Fatalf("no hosts: %v", err)
	}
	if err := client.SaveHosts(context.Background(), []client.Host{{Name: "a", SSH: "a"}}); err != nil {
		t.Fatal(err)
	}
	if h, err := pickHost(""); err != nil || h.Name != "a" {
		t.Fatalf("one host: %v %v", h, err)
	}
	_ = client.SaveHosts(context.Background(), []client.Host{{Name: "a", SSH: "a"}, {Name: "b", SSH: "b"}})
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

// The host of agents install and agents login: the second argument, --host, or
// the only saved host (cli-spec 12.1).
func TestAgentHost(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, verb := range []string{"install", "login"} {
		if got, code := lineErr(t, "agents", verb, "claude"); code != 1 || !strings.Contains(got, "no hosts yet") {
			t.Errorf("agents %s with no hosts: exit %d\n%s", verb, code, got)
		}
	}
	if err := client.SaveHosts(t.Context(), []client.Host{{Name: "box", SSH: "box"}, {Name: "nuc", SSH: "nuc"}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		args []string
		code int
		has  []string
	}{
		{[]string{"agents", "login", "claude"}, 2, []string{"Error: agents login needs a host: you have box, nuc.", "Try: " + brand.Name + " agents login claude box\n"}},
		{[]string{"agents", "install", "claude", "--reinstall"}, 2, []string{"agents install needs a host: you have box, nuc", "Try: " + brand.Name + " agents install claude box --reinstall\n"}},
		{[]string{"--no-input", "agents", "install", "codex"}, 2, []string{"Try: " + brand.Name + " --no-input agents install codex box\n"}},
		{[]string{"agents", "login", "claude", "box", "--host", "nuc"}, 2, []string{"agents login got two hosts: box, and nuc with --host", "Try: Name the host once: as the second word, or with --host."}},
		{[]string{"agents", "login", "claude", "boxe"}, 3, []string{`no host named "boxe"`, `Did you mean "box"?`, "Try: " + brand.Name + " agents login claude box\n"}},
		{[]string{"agents", "install", "claude", "--host", "nucc"}, 3, []string{`Did you mean "nuc"?`, "Try: " + brand.Name + " agents install claude --host nuc\n"}},
		{[]string{"agents", "install", "shell", "box"}, 2, []string{"shell has no install command", "Try: " + brand.Name + " agents ls\n"}},
		{[]string{"agents", "login", "claude", "box", "nuc"}, 2, []string{"got 3 words"}},
	} {
		got, code := lineErr(t, c.args...)
		if code != c.code {
			t.Errorf("%v: exit %d, want %d\n%s", c.args, code, c.code, got)
		}
		for _, want := range c.has {
			if !strings.Contains(got, want) {
				t.Errorf("%v: missing %q in\n%s", c.args, want, got)
			}
		}
	}
}

// agents install --force is a hidden old name for --reinstall (cli-spec 12.2).
func TestAgentsInstallOldForce(t *testing.T) {
	_, tr := fakeBox(t, nil)
	tr.SetAgents([]api.AgentCheck{claudeCheck(api.LoginYes, "")})
	out, errOut, code := mainRun(t, "agents", "install", "claude", "box", "--force", "--no-input")
	if code != 0 || out != "" {
		t.Fatalf("agents install --force: exit %d\nstdout %q\nstderr %s", code, out, errOut)
	}
	for _, want := range []string{
		`Warning: "--force" is now "--reinstall".`,
		"Installing Claude Code on box",
		"Started box/fake2.",
	} {
		if !strings.Contains(errOut, want) {
			t.Errorf("agents install --force lacks %q in:\n%s", want, errOut)
		}
	}
	if starts(tr) != 1 {
		t.Errorf("starts = %d, want 1", starts(tr))
	}
	if oldInstallForce.Old != "--force" || oldInstallForce.Cmd != "agents install" {
		t.Errorf("oldInstallForce = %+v", *oldInstallForce)
	}
	help, err := run(t, "agents", "install", "--help")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(help, "--force") || !strings.Contains(help, "--reinstall") {
		t.Errorf("help shows --force or hides --reinstall:\n%s", help)
	}
}

// After an install, the client checks the agent, and the line after names
// agents login (cli-spec 12.2, rule 4).
func TestAfterInstallNamesLogin(t *testing.T) {
	h, tr := fakeBox(t, nil)
	for _, c := range []struct {
		kind, login string
		has         []string
		not         string
	}{
		{"claude", api.LoginNo, []string{"! Claude Code", "\nNext, log in: " + brand.Name + " agents login claude box\n"}, "Try:"},
		{"claude", api.LoginYes, []string{"✓ Claude Code"}, "Next, log in"},
		{"aider", api.LoginNo, []string{"! Aider", "Try: pass its key from your laptop"}, "Next, log in"},
	} {
		a, _ := agents.Find(c.kind)
		tr.SetAgents([]api.AgentCheck{readyCheck(a.Kind, a.Title, c.login)})
		errOut, err := said(t, func() error { return afterInstall(t.Context(), h, a, false) })
		if err != nil {
			t.Fatalf("%s: %v", c.kind, err)
		}
		for _, want := range c.has {
			if !strings.Contains(errOut, want) {
				t.Errorf("%s, login %s: no %q in\n%s", c.kind, c.login, want, errOut)
			}
		}
		if strings.Contains(errOut, c.not) {
			t.Errorf("%s, login %s: %q in\n%s", c.kind, c.login, c.not, errOut)
		}
	}
}

func TestInstallableAgentsHaveALoginCommand(t *testing.T) {
	for _, a := range agents.Installable() {
		if a.Setup.Login == "" && len(a.Setup.LoginEnv) == 0 {
			t.Errorf("%s: say how to log in, or which variable holds its key", a.Kind)
		}
	}
}

func TestAfterInstallRebuildsAgentsDisk(t *testing.T) {
	th := newTestHost(t)
	t.Setenv("SHELL", "/bin/sh")
	home := os.Getenv("HOME")
	verDir := filepath.Join(home, ".local", "share", "claude", "versions")
	localBin := filepath.Join(home, ".local", "bin")
	for _, d := range []string{verDir, localBin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	v1 := filepath.Join(verDir, "1.0")
	if err := os.WriteFile(v1, []byte("#!/bin/sh\necho claude-v1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	claudeLink := filepath.Join(localBin, "claude")
	if err := os.Symlink(v1, claudeLink); err != nil {
		t.Fatal(err)
	}
	a, ok := agents.Find("claude")
	if !ok {
		t.Fatal("claude missing")
	}
	if _, err := said(t, func() error { return afterInstall(t.Context(), th.ch, a, false) }); err != nil {
		t.Fatalf("afterInstall 1: %v", err)
	}
	rec1, err := host.ImageStore().LatestAgentsDisk()
	if err != nil {
		t.Fatalf("LatestAgentsDisk after install 1: %v", err)
	}
	if rec1.Digest == "" {
		t.Fatalf("empty digest after install 1: %+v", rec1)
	}

	// Simulate reinstalling a newer version of claude on the host.
	v2 := filepath.Join(verDir, "2.0")
	if err := os.WriteFile(v2, []byte("#!/bin/sh\necho claude-v2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(claudeLink)
	if err := os.Symlink(v2, claudeLink); err != nil {
		t.Fatal(err)
	}
	if _, err := said(t, func() error { return afterInstall(t.Context(), th.ch, a, false) }); err != nil {
		t.Fatalf("afterInstall 2: %v", err)
	}
	rec2, err := host.ImageStore().LatestAgentsDisk()
	if err != nil {
		t.Fatalf("LatestAgentsDisk after install 2: %v", err)
	}
	if rec2.Digest == rec1.Digest {
		t.Fatalf("expected new agents disk digest after reinstall, still %s", rec1.Digest)
	}
}
