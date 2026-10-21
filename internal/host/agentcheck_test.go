package host

import (
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
)

// fakeAgent writes a program that exits with code when called as "<name>
// status", into dir, and returns a table row for it.
func fakeAgent(t *testing.T, dir, name string, code int) agents.Agent {
	t.Helper()
	script := "#!/bin/sh\n[ \"$1\" = status ] && exit " + string(rune('0'+code)) + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return agents.Agent{Kind: name, Title: "Fake " + name, Binaries: []string{name}, Support: agents.FirstClass,
		Setup: &agents.Setup{Install: "true", LoginStatus: []string{name, "status"}}}
}

func TestCheckAgentLogin(t *testing.T) {
	home, bin := t.TempDir(), t.TempDir()
	env := []string{"PATH=" + bin + ":/usr/bin:/bin"}

	in := fakeAgent(t, bin, "inagent", 0)
	if c := checkAgent(in, env, home); !c.Installed || !c.OnPath || c.Login != api.LoginYes {
		t.Fatalf("logged-in agent: %+v", c)
	}
	out := fakeAgent(t, bin, "outagent", 1)
	out.Setup.LoginFiles = []string{"~/.outagent/creds"}
	_ = os.MkdirAll(filepath.Join(home, ".outagent"), 0o700)
	_ = os.WriteFile(filepath.Join(home, ".outagent", "creds"), []byte("{}"), 0o600)
	if c := checkAgent(out, env, home); c.Login != api.LoginNo {
		t.Fatalf("the status command says not, so a stale file does not count: %+v", c)
	}
	// An exit code other than 0 or 1 (an old version without the command)
	// falls back to the files.
	old := fakeAgent(t, bin, "oldagent", 2)
	old.Setup.LoginFiles = out.Setup.LoginFiles
	if c := checkAgent(old, env, home); c.Login != api.LoginYes || c.LoginHow != "found ~/.outagent/creds" {
		t.Fatalf("old agent with a credentials file: %+v", c)
	}
	old.Setup.LoginFiles = []string{"~/.none"}
	if c := checkAgent(old, env, home); c.Login != api.LoginNo {
		t.Fatalf("old agent without a credentials file: %+v", c)
	}
	// A variable that logs the agent in is enough.
	old.Setup.LoginEnv = []string{"FAKE_KEY"}
	if c := checkAgent(old, append(env, "FAKE_KEY=x"), home); c.Login != api.LoginYes || c.LoginHow != "FAKE_KEY is set in the login shell" {
		t.Fatalf("agent with its key in the environment: %+v", c)
	}
	// With nothing to read, the login is unknown.
	unk := fakeAgent(t, bin, "unkagent", 0)
	unk.Setup.LoginStatus = nil
	if c := checkAgent(unk, env, home); c.Login != api.LoginUnknown {
		t.Fatalf("agent with no login data: %+v", c)
	}
}

func TestAgentVersions(t *testing.T) {
	bin := t.TempDir()
	// prog writes a program that runs script when called with --version.
	prog := func(name, script string) string {
		p := filepath.Join(bin, name)
		s := "#!/bin/sh\n[ \"$1\" = --version ] || exit 2\n" + script + "\n"
		if err := os.WriteFile(p, []byte(s), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	got := AgentVersions(map[string]string{
		"claude": prog("claude", `echo "2.1.3 (Claude Code)"`),
		"codex":  prog("codex", `echo "codex-cli 0.46.0"`),
		"aider":  prog("aider", `echo; echo "aider 0.86.1"`),
		"cursor": prog("agent", `echo "2025.10.02-a1b2c3d"`),
		"child":  prog("child", `sleep 3 & echo "1.4.0"`),
		"failed": prog("failed", `echo "1.0.0"; exit 1`),
		"none":   prog("none", `echo "usage: none [flags]"`),
	}, []string{"PATH=/usr/bin:/bin"})
	want := map[string]string{"claude": "2.1.3", "codex": "0.46.0", "aider": "0.86.1", "cursor": "2025.10.02-a1b2c3d", "child": "1.4.0"}
	if !maps.Equal(got, want) {
		t.Fatalf("versions: got %v, want %v", got, want)
	}
}

func TestCheckAgentPath(t *testing.T) {
	home := t.TempDir()
	a := agents.Agent{Kind: "x", Binaries: []string{"xagent"}, Support: agents.Supported,
		Setup: &agents.Setup{Install: "true", Paths: []string{"~/.local/bin/xagent"}}}
	if c := checkAgent(a, []string{"PATH=/usr/bin:/bin"}, home); c.Installed || c.Login != api.LoginUnknown {
		t.Fatalf("not installed: %+v", c)
	}
	p := filepath.Join(home, ".local", "bin", "xagent")
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	_ = os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755)
	c := checkAgent(a, []string{"PATH=/usr/bin:/bin"}, home)
	if !c.Installed || c.OnPath || c.Path != p {
		t.Fatalf("installed off PATH: %+v", c)
	}
}
