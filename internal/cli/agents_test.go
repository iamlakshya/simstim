package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
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

// An agent's line holds the host's text, which prints cleaned, as host
// doctor and the guided setup print it too.
func TestPrintAgentChecksCleansTheHostsText(t *testing.T) {
	var b bytes.Buffer
	printAgentChecks(&b, "vps", []api.AgentCheck{{Kind: "codex", Title: "Codex\x1b]52;c;aGk=\x07", Installed: true, OnPath: true,
		Path: "/p\x1b[2J/codex", Login: api.LoginNo, LoginHow: "says\u202e not\n"}})
	if out := b.String(); strings.ContainsAny(out, "\x1b\x07\u202e") || !strings.Contains(out, "! Codex]52;c;aGk=  /p[2J/codex, not logged in (says not)") {
		t.Errorf("printed %q", out)
	}
	// A line that Describe did not make, such as the guided setup's note
	// with a session that the host named, prints cleaned too.
	b.Reset()
	printLine(&b, onboard.Line{Mark: "!", Title: "Codex\x1b[2J", State: "s\x07", Try: "its login still runs: attach box/a\x1b]52;c;aGk=\x07"})
	if out := b.String(); strings.ContainsAny(out, "\x1b\x07") || !strings.Contains(out, "    Try: its login still runs: attach box/a]52;c;aGk=\n") {
		t.Errorf("printLine printed %q", out)
	}
}

// The host of agents install and agents login: the second argument,
// --host, or the only saved host (cli-spec 12.1).
func TestAgentHost(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, verb := range []string{"install", "login"} {
		if got, code := lineErr(t, "agents", verb, "claude"); code != 1 || !strings.Contains(got, "no hosts yet") {
			t.Errorf("agents %s with no hosts: exit %d\n%s", verb, code, got)
		}
	}
	if err := client.SaveHostsContext(context.Background(), []client.Host{{Name: "a", SSH: "a"}}); err != nil {
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
		{[]string{"agents", "login", "claude", "box", "nuc"}, 2, []string{"agents login takes <agent> [<host>], and got 3 words"}},
		{[]string{"agents", "login"}, 2, []string{"agents login needs <agent>", "Example: " + brand.Name + " agents login claude my-vps"}},
	} {
		got, code := lineErr(t, c.args...)
		if code != c.code {
			t.Errorf("%s exits with %d, want %d:\n%s", strings.Join(c.args, " "), code, c.code, got)
		}
		for _, h := range c.has {
			if !strings.Contains(got, h) {
				t.Errorf("%s printed\n%s\nwhich lacks %q", strings.Join(c.args, " "), got, h)
			}
		}
	}
	_ = client.SaveHostsContext(context.Background(), []client.Host{{Name: "a", SSH: "a"}, {Name: "b", SSH: "b"}})
	_, err := pickHost("")
	var ue *client.UserError
	if err == nil || !strings.Contains(err.Error(), "which host? You have a, b") {
		t.Fatalf("two hosts: %v", err)
	}
	if got := orList([]string{"A", "B", "C"}); got != "A, B or C" {
		t.Errorf("orList = %q", got)
	}
}

// --force is the old name of --reinstall: it runs the installer again, and
// warns once (cli-spec 3.2 and 3.3).
func TestAgentsInstallForceIsOld(t *testing.T) {
	_, tr := withFake(t, nil)
	t.Setenv("NO_COLOR", "1")
	tr.SetAgents([]api.AgentCheck{readyCheck("claude", "Claude Code", api.LoginYes)})
	if err := saveHost(client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}); err != nil {
		t.Fatal(err)
	}
	out, errOut, code := mainRun(t, "agents", "install", "claude", "box")
	if code != 0 || out != "" || !strings.Contains(errOut, "is already installed on box") || !strings.Contains(errOut, "add --reinstall.") {
		t.Fatalf("an installed agent: exit %d\nstdout %q\nstderr %s", code, out, errOut)
	}
	for _, flag := range []string{"--reinstall", "--force"} {
		out, errOut, code = mainRun(t, "agents", "install", "claude", "box", flag)
		if code != 0 || out != "" || !regexp.MustCompile(`Started box/fake[12]\. Attach to answer its questions`).MatchString(errOut) {
			t.Errorf("agents install %s: exit %d\nstdout %q\nstderr %s", flag, code, out, errOut)
		}
		warned := strings.Count(errOut, `Warning: "--force" is now "--reinstall". "--force" works until v0.4.0 and `)
		if want := map[string]int{"--reinstall": 0, "--force": 1}[flag]; warned != want {
			t.Errorf("agents install %s warned %d times:\n%s", flag, warned, errOut)
		}
	}
	if oldInstallForce.Old != "--force" || oldInstallForce.Cmd != "agents install" {
		t.Errorf("oldInstallForce is the row %+v", *oldInstallForce)
	}
	help, _ := run(t, "agents", "install", "--help")
	if strings.Contains(help, "--force") || !strings.Contains(help, "--reinstall") {
		t.Errorf("the help shows the old name, or not the new one:\n%s", help)
	}
}

// After an install, the client checks the agent, and the line after names
// agents login (cli-spec 12.2, rule 4).
func TestAfterInstallNamesLogin(t *testing.T) {
	_, tr := withFake(t, nil)
	box := client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}
	if err := saveHost(box); err != nil {
		t.Fatal(err)
	}
	h, _ := client.FindHost("box")
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
		errOut, err := said(t, func() error { return afterInstall(t.Context(), h, a) })
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
