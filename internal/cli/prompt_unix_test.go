//go:build unix

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

// promptIn asks one question on a pseudo-terminal, as a person sees it, and
// returns the answer and what the terminal showed.
func promptIn(t *testing.T, keys string, ask func(p *prompter) (bool, error)) (bool, string, error) {
	t.Helper()
	term := hosttest.NewTerminal(t, 80, 10)
	p := &prompter{ctx: context.Background(), in: term.TTY, out: term.TTY, on: true}
	type result struct {
		ok  bool
		err error
	}
	done := make(chan result, 1)
	go func() {
		ok, err := ask(p)
		done <- result{ok, err}
	}()
	term.WaitFor("] ")
	for _, k := range strings.Split(keys, "|") {
		term.Type(k)
		time.Sleep(20 * time.Millisecond) // each key in its own read, as a person types
	}
	select {
	case r := <-done:
		return r.ok, string(term.Output()), r.err
	case <-time.After(10 * time.Second):
		t.Fatalf("the prompt never ended; the terminal shows %q", term.Output())
		return false, "", nil
	}
}

// Yes, no, the default, three bad answers, esc and ctrl+c, on a terminal.
func TestYesNoOnATerminal(t *testing.T) {
	q := func(def bool) func(p *prompter) (bool, error) {
		return func(p *prompter) (bool, error) { return p.yesNo("Delete it?", def) }
	}
	for _, c := range []struct {
		name, keys string
		def        bool
		want       bool
		cancel     bool
	}{
		{"yes", "y|\r", false, true, false},
		{"YES in capitals", "Y|E|S|\r", false, true, false},
		{"no", "n|o|\r", true, false, false},
		{"Enter takes the default no", "\r", false, false, false},
		{"Enter takes the default yes", "\r", true, true, false},
		{"backspace edits", "n|\x7f|y|\r", false, true, false},
		{"an arrow does nothing", "\x1b[A|y|\r", false, true, false},
		{"three bad answers cancel", "x|\r|m|a|y|b|e|\r|?|\r", false, false, true},
		{"esc cancels", "\x1b", false, false, true},
		{"ctrl+c cancels", "\x03", true, false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, screen, err := promptIn(t, c.keys, q(c.def))
			if c.cancel {
				if !isCancel(err) || !errors.Is(err, context.Canceled) {
					t.Fatalf("err = %v, want a cancel; the terminal shows %q", err, screen)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("got %v, %v, want %v; the terminal shows %q", got, err, c.want, screen)
			}
			opts := "[y/N]"
			if c.def {
				opts = "[Y/n]"
			}
			if !strings.Contains(screen, "Delete it? "+opts+" ") {
				t.Fatalf("the question was %q", screen)
			}
		})
	}
}

// The question of a severe action needs the typed name: the exact name
// confirms, Enter keeps the thing, and any other text cancels.
func TestTypedName(t *testing.T) {
	ask := func(p *prompter) (bool, error) {
		return p.typedName("Type box to delete the machine, or press Enter to keep it: [box]", "box")
	}
	for _, c := range []struct {
		keys   string
		want   bool
		cancel bool
	}{
		{"b|o|x|\r", true, false},
		{"\r", false, false},
		{"B|o|x|\r", false, true},
		{"b|o|\r", false, true},
		{"\x1b", false, true},
		// Only the exact name: spaces around it, or spaces alone, cancel.
		{" |b|o|x|\r", false, true},
		{"b|o|x| |\r", false, true},
		{" | |\r", false, true},
	} {
		got, screen, err := promptIn(t, c.keys, ask)
		if c.cancel != isCancel(err) || got != c.want || (err != nil && !c.cancel) {
			t.Errorf("%q: got %v, %v, want %v (cancel %v); the terminal shows %q", c.keys, got, err, c.want, c.cancel, screen)
		}
	}
}

// Each command that asks, run with --no-input: it fails with exit code 4
// and names the flag, or takes the default and says so on stderr.
func TestCommandsWithNoInput(t *testing.T) {
	newTestHost(t)

	// new: a missing folder needs --mkdir.
	got, code := lineErr(t, "new", "box", "--dir", "~/not-there", "--no-input", "--detach", "--wall", "none", "--downgrade")
	if code != 4 || !strings.Contains(got, "Try: "+brand.Name+" new box --dir '~/not-there' --no-input --detach --wall none --downgrade --mkdir") {
		t.Errorf("new with a missing folder: exit %d\n%s", code, got)
	}

	// host doctor --fix: each fix needs -y.
	old := time.Now().Add(-2 * time.Hour)
	for _, v := range []string{"v0.1.0", "v0.2.0", "v0.3.0", "v0.4.0"} {
		dir := filepath.Join(os.Getenv("XDG_DATA_HOME"), brand.Name, "bin", v)
		_ = os.MkdirAll(dir, 0o700)
		_ = os.Chtimes(dir, old, old)
	}
	got, code = lineErr(t, "host", "doctor", "box", "--fix", "--no-input")
	if code != 4 || !strings.Contains(got, "host doctor needs a yes for each fix, and input is off") || !strings.Contains(got, "Try: "+brand.Name+" host doctor box --fix --no-input -y") {
		t.Errorf("host doctor --fix: exit %d\n%s", code, got)
	}

}

// With input off, new skips a repository's setup script that nobody
// allowed, and says so on stderr, with the flag that runs it: one line, or
// one JSON line with --json.
func TestNewSkipsTheSetupScriptWithInputOff(t *testing.T) {
	newTestHost(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com", "GIT_CONFIG_GLOBAL": os.DevNull} {
		t.Setenv(k, v)
	}
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	repo := filepath.Join(dir, "app")
	hook := filepath.Join(repo, "."+brand.Name, "setup")
	_ = os.MkdirAll(filepath.Dir(hook), 0o755)
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch \"$HOME/setup-ran\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"commit", "-q", "-m", "init"}} {
		c := exec.Command("git", args...)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	_, errOut, err := runApart(t, "new", "box", "--dir", repo, "--detach", "--wall", "none", "--downgrade", "--no-input", "--", "sleep", "60")
	if err != nil || !strings.Contains(errOut, "Skipping the setup script of "+repo+": you have not said that it may run on box. Pass --setup-script to run it.") {
		t.Fatalf("new --no-input: %v\n%s", err, errOut)
	}
	out, errOut, err := runApart(t, "new", "box", "--dir", repo, "--detach", "--wall", "none", "--downgrade", "--json", "--", "sleep", "60")
	sawSetupSkipped := false
	for _, line := range strings.Split(strings.TrimSpace(errOut), "\n") {
		if line == "" {
			continue
		}
		var warn struct {
			V       int
			Warning map[string]string
		}
		if err := json.Unmarshal([]byte(line), &warn); err != nil {
			t.Fatalf("new --json non-JSON stderr line %q: %v", line, err)
		}
		if warn.Warning["code"] == "setup_skipped" {
			sawSetupSkipped = true
		}
	}
	if err != nil || !sawSetupSkipped || !json.Valid([]byte(out)) {
		t.Fatalf("new --json: %v\nstdout %s\nstderr %s", err, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), "setup-ran")); err == nil {
		t.Error("the setup script ran")
	}
}
