//go:build unix

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/brand"
)

// fakeClaude puts a fake claude on the test host, in a folder that the
// login shell's profile puts on PATH. Its login prints a line, asks for a
// code, and keeps it. Its status says whether it has a code.
func fakeClaude(t *testing.T, th *testHost) {
	t.Helper()
	claude, _ := agents.Find("claude")
	for _, k := range claude.Setup.LoginEnv {
		t.Setenv(k, "") // a key of the developer's would log the fake in
	}
	if err := os.WriteFile(filepath.Join(th.h.Home, ".profile"), []byte("PATH=\"$HOME/bin:$PATH\"; export PATH\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(th.h.Home, "bin", "claude")
	fake := `#!/bin/sh
case "$1 $2" in
"auth status") test -e "$HOME/.fake-login" ;;
"auth login")
	echo "FAKE-LOGIN-START $*"
	printf 'FAKE-CODE-PROMPT: '
	read code
	echo "$code" > "$HOME/.fake-login"
	echo FAKE-LOGGED-IN ;;
*) exit 2 ;;
esac
`
	if err := os.MkdirAll(filepath.Dir(bin), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte(fake), 0o700); err != nil {
		t.Fatal(err)
	}
}

// The acceptance line of C8: agents login claude box starts the documented
// login of Claude Code on box, `claude auth login`, in a session that the
// user answers. Here box is the host code on this machine, with a fake
// claude that a login shell finds. A detach leaves the login running, and
// a second agents login attaches to it. The answer ends the login, and the
// check then finds Claude Code logged in.
func TestAgentsLoginClaude(t *testing.T) {
	th := newTestHost(t)
	fakeClaude(t, th)

	term, done := inTerminal(t, "agents", "login", "claude", "box")
	term.WaitFor("FAKE-CODE-PROMPT:") // the agent's first prompt
	out := string(term.Output())
	m := loginStartedRE.FindStringSubmatch(out)
	if m == nil || !strings.Contains(out, "claude auth login") || !strings.Contains(out, "FAKE-LOGIN-START auth login") {
		t.Fatalf("the login did not start as documented:\n%s", out)
	}
	id := m[1]
	term.Type("\x1c")
	if err := wait(t, done); err != nil {
		t.Fatalf("agents login: %v\n%s", err, term.Output())
	}
	term.WaitFor("The login keeps running. Attach again: " + brand.Name + " attach --id -- box/" + id)
	if out := string(term.Output()); !strings.Contains(out, "! Claude Code") || strings.Contains(out, "Try:") {
		t.Errorf("after a detach:\n%s", out)
	}

	// At a terminal, with input off, it finds the login that runs, and
	// says how to attach to it.
	noIn, done := inTerminal(t, "agents", "login", "claude", "box", "--no-input")
	if err := wait(t, done); err != nil {
		t.Fatalf("agents login --no-input: %v\n%s", err, noIn.Output())
	}
	noIn.WaitFor("The login of Claude Code already runs in box/" + id + ". Attach to finish the login: " + brand.Name + " attach --id -- box/" + id)

	term, done = inTerminal(t, "agents", "login", "claude", "box")
	term.WaitFor("The login of Claude Code already runs in box/" + id + ". Attaching to it.")
	term.WaitFor("FAKE-CODE-PROMPT:")
	term.Type("a-code\r")
	if err := wait(t, done); err != nil {
		t.Fatalf("agents login again: %v\n%s", err, term.Output())
	}
	term.WaitFor("✓ Claude Code")
	if out := string(term.Output()); strings.Contains(out, "keeps running") {
		t.Errorf("after the end of the login:\n%s", out)
	}
	if got, _ := os.ReadFile(filepath.Join(th.h.Home, ".fake-login")); strings.TrimSpace(string(got)) != "a-code" {
		t.Errorf("the login got %q", got)
	}
}
