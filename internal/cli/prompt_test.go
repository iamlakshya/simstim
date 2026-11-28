package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/helpers"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

// The variable turns input off with any value but "", "0" and "false"
// (docs/contributing/cli-spec.md 6.4).
func TestNoInputVariable(t *testing.T) {
	for v, off := range map[string]bool{"": false, "0": false, "false": false, "1": true, "yes": true, "true": true} {
		if got := envTurnsInputOff(v); got != off {
			t.Errorf("%s=%q turns input off: %v, want %v", noInputEnv, v, got, off)
		}
		t.Setenv(noInputEnv, v)
		if got := inputOff(nil); got != off {
			t.Errorf("inputOff with %s=%q = %v", noInputEnv, v, got)
		}
	}
	if err := os.Unsetenv(noInputEnv); err != nil {
		t.Fatal(err)
	}
	if inputOff(nil) {
		t.Error("input is off with the variable unset")
	}
}

// --no-input and --json turn input off, and so does a stream that is not a
// terminal.
func TestInputOffByFlags(t *testing.T) {
	t.Setenv(noInputEnv, "")
	root := Root()
	for _, c := range []struct {
		args []string
		off  bool
	}{
		{[]string{"ls"}, false},
		{[]string{"ls", "--no-input"}, true},
		{[]string{"ls", "--json"}, true},
		{[]string{"--no-input", "host", "ls"}, true},
	} {
		cmd, rest, err := root.Find(c.args)
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.ParseFlags(rest); err != nil {
			t.Fatal(err)
		}
		if got := inputOff(cmd); got != c.off {
			t.Errorf("%s: input off = %v, want %v", c.args, got, c.off)
		}
		// The test's own streams are not a terminal.
		if inputOn(cmd) {
			t.Errorf("%s: input on without a terminal", c.args)
		}
	}
}

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

// Without a terminal too, only the exact name confirms: the line ending
// goes, and nothing else. Enter keeps the thing.
func TestTypedNameFromAPipe(t *testing.T) {
	for in, want := range map[string]struct{ ok, cancel bool }{
		"box\n":   {true, false},
		"box\r\n": {true, false},
		" box\n":  {false, true},
		"box \n":  {false, true},
		"box\t\n": {false, true},
		"   \n":   {false, true},
		"\t\n":    {false, true},
		"\n":      {false, false},
		"\r\n":    {false, false},
	} {
		var out bytes.Buffer
		ok, err := testPrompts(in, &out).typedName("Type box to delete it, or press Enter to keep it:", "box")
		if ok != want.ok || isCancel(err) != want.cancel || err != nil && !want.cancel {
			t.Errorf("%q: %v, %v", in, ok, err)
		}
	}
}

// Without a terminal, a prompt reads a byte at a time, and the end of input
// or esc cancels.
func TestPromptWithoutATerminal(t *testing.T) {
	for in, want := range map[string]string{"y\n": "y", "  no  \n": "no", "last": "last", "\n": ""} {
		var out bytes.Buffer
		if got, err := testPrompts(in, &out).ask("Q? "); err != nil || got != want {
			t.Errorf("%q: %q, %v, want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "\x1b\n"} {
		var out bytes.Buffer
		if _, err := testPrompts(in, &out).ask("Q? "); !isCancel(err) {
			t.Errorf("%q: %v, want a cancel", in, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := testPrompts("y\n", &bytes.Buffer{})
	p.ctx = ctx
	if _, err := p.ask("Q? "); !errors.Is(err, context.Canceled) {
		t.Errorf("a stopped context: %v", err)
	}
}

// The prompt table of cli-spec 6.2 has a row for each question in the code,
// and the flag of each row exists on its command.
func TestEveryPromptHasARow(t *testing.T) {
	spec, err := os.ReadFile(filepath.Join("..", "..", "docs", "contributing", "cli-spec.md"))
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(spec), "**The prompt table.**")
	end := strings.Index(string(spec), "### 6.3 Pickers")
	if start < 0 || end < start {
		t.Fatal("the spec has no prompt table")
	}
	var rows []string
	for _, l := range strings.Split(string(spec)[start:end], "\n") {
		if strings.HasPrefix(l, "| `") {
			rows = append(rows, l)
		}
	}
	root := Root()
	code := regexp.MustCompile("`[^`]*`")
	for _, q := range questions {
		cmd, rest, err := root.Find(strings.Fields(q.cmd))
		if err != nil || len(rest) != 0 || cmd == root {
			t.Errorf("%q: no such command", q.cmd)
			continue
		}
		var f string
		if strings.HasPrefix(q.flag, "--") {
			if cmd.Flags().Lookup(strings.TrimPrefix(q.flag, "--")) != nil {
				f = q.flag
			}
		} else if fl := cmd.Flags().ShorthandLookup(strings.TrimPrefix(q.flag, "-")); fl != nil {
			f = q.flag
		}
		if f == "" {
			t.Errorf("%s: no flag %s", q.cmd, q.flag)
		}
		found := false
		for _, r := range rows {
			cells := strings.Split(r, "|")
			if len(cells) < 6 {
				continue
			}
			names := false
			for _, c := range code.FindAllString(cells[1], -1) {
				if w := strings.Fields(strings.Trim(c, "`")); len(w) >= len(strings.Fields(q.cmd)) && strings.Join(w[:len(strings.Fields(q.cmd))], " ") == q.cmd {
					names = true
				}
			}
			if names && strings.Contains(cells[4], q.flag) && strings.Contains(cells[5], map[bool]string{true: "Keep", false: "Fail"}[q.keep]) {
				found = true
			}
		}
		if !found {
			t.Errorf("the prompt table of cli-spec 6.2 has no row for %s with %s and %q", q.cmd, q.flag, map[bool]string{true: "Keep", false: "Fail"}[q.keep])
		}
	}
}

// With input off, each question fails with exit code 4 and names its flag,
// or takes its default and says so (cli-spec 6.4).
func TestPromptsWithInputOff(t *testing.T) {
	for _, q := range questions {
		var out bytes.Buffer
		p := &prompter{ctx: context.Background(), in: strings.NewReader("y\n"), out: &out, line: strings.Fields(q.cmd), flagsAt: len(strings.Fields(q.cmd))}
		ok, err := p.confirm(q, "Go on?", "a yes to go on")
		if out.Len() != 0 {
			t.Errorf("%s: asked %q with input off", q.cmd, out.String())
		}
		if q.keep {
			if err != nil || ok != q.def {
				t.Errorf("%s: %v %v, want the default", q.cmd, ok, err)
			}
			continue
		}
		var ue *client.UserError
		if !errors.As(err, &ue) || ue.Kind != client.KindNeedsAnswer || exitCode(ue.Kind) != 4 || !strings.HasSuffix(ue.Hint, " "+q.flag) {
			t.Errorf("%s: %+v, want exit code 4 and %s", q.cmd, err, q.flag)
		}
	}
}

// Each command that asks, run with --no-input: it fails with exit code 4
// and names the flag, or takes the default and says so on stderr.
func TestCommandsWithNoInput(t *testing.T) {
	newTestHost(t)

	// new: a missing folder needs --mkdir.
	got, code := lineErr(t, "new", "box", "--dir", "~/not-there", "--no-input", "--detach")
	if code != 4 || !strings.Contains(got, "Try: "+brand.Name+" new box --dir '~/not-there' --no-input --detach --mkdir") {
		t.Errorf("new with a missing folder: exit %d\n%s", code, got)
	}

	// doctor --fix: each fix needs -y.
	_ = os.MkdirAll(filepath.Join(client.ConfigDir(), "ssh"), 0o700)
	_ = os.WriteFile(filepath.Join(client.ConfigDir(), "ssh", "config-gone"), []byte("# generated\n"), 0o600)
	got, code = lineErr(t, "doctor", "--fix", "--no-input")
	if code != 4 || !strings.Contains(got, "doctor needs a yes for each fix, and input is off") || !strings.Contains(got, "Try: "+brand.Name+" doctor --fix --no-input -y") {
		t.Errorf("doctor --fix: exit %d\n%s", code, got)
	}
	if _, err := os.Stat(filepath.Join(client.ConfigDir(), "ssh", "config-gone")); err != nil {
		t.Error("doctor --fix with input off removed the file")
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

	// setup: its flags must answer every question.
	if _, code = lineErr(t, "setup", "--no-input"); code != 4 {
		t.Errorf("setup --no-input exits with %d", code)
	}

	// The variable does the same as the flag.
	t.Setenv(noInputEnv, "1")
	if _, code = lineErr(t, "doctor", "--fix"); code != 4 {
		t.Errorf("doctor --fix with %s=1 exits with %d", noInputEnv, code)
	}
}

// host destroy needs -y with input off, and leaves the host untouched on
// no or when input is off.
func TestHostQuestionsWithInputOff(t *testing.T) {
	withFake(t, nil)
	if err := saveHost(context.Background(), client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}); err != nil {
		t.Fatal(err)
	}
	p := quietPrompts(context.Background())
	p.line, p.flagsAt = []string{"host", "destroy", "box"}, 3
	err := destroyHost(context.Background(), p, "box", false, false)
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Kind != client.KindNeedsAnswer || ue.Hint != brand.Name+" host destroy box -y" {
		t.Fatalf("destroy with input off: %+v", err)
	}
	if _, err := client.FindHost("box"); err != nil {
		t.Fatal("the host was forgotten")
	}
	// Enter or wrong text is a cancel, and nothing changes.
	var out bytes.Buffer
	if err := destroyHost(context.Background(), testPrompts("\n", &out), "box", false, false); !isCancel(err) {
		t.Fatalf("destroy, Enter: %v", err)
	}
	if !strings.Contains(out.String(), "Type box to delete the machine, or press Enter to keep it: ") {
		t.Fatalf("destroy asked %q", out.String())
	}
}

// addHost asks before it installs the helper: with input off it fails with
// exit code 4 and names --yes, and a no is a cancel. The command itself
// runs in TestHostAddNoInputCommand.
func TestHostAddAsksBeforeTheHelper(t *testing.T) {
	_, tr := withFake(t, nil)
	tr.NoHelper = true
	if _, ok := helpers.For("linux", "arm64"); !ok {
		t.Skip("this build has no helper for linux/arm64 (run just helpers)")
	}
	box := client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}
	p := quietPrompts(context.Background())
	p.line, p.flagsAt = []string{"host", "add", "box", "--no-input"}, 4
	err := addHost(context.Background(), p, box, false)
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Kind != client.KindNeedsAnswer || exitCode(ue.Kind) != 4 ||
		ue.Hint != brand.Name+" host add box --no-input --yes" || ue.Msg != "host add needs a yes to install the helper on box, and input is off" {
		t.Fatalf("host add with input off: %+v", err)
	}
	if _, err := client.FindHost("box"); err == nil {
		t.Fatal("the host was saved")
	}
	for _, r := range tr.Remotes() {
		if strings.Contains(r, "base64") || strings.Contains(r, " bridge") {
			t.Fatalf("it went on after the question: %s", r)
		}
	}
	// A no at a terminal is a cancel, and nothing is installed.
	var out bytes.Buffer
	if err := addHost(context.Background(), testPrompts("n\n", &out), box, false); !isCancel(err) || !strings.Contains(out.String(), "Install it? [y/N] ") {
		t.Fatalf("a no: %v\n%s", err, out.String())
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
	_, errOut, err := runApart(t, "new", "box", "--dir", repo, "--detach", "--no-input", "--", "sleep", "60")
	if err != nil || !strings.Contains(errOut, "Skipping the setup script of "+repo+": you have not said that it may run on box. Pass --setup to run it.") {
		t.Fatalf("new --no-input: %v\n%s", err, errOut)
	}
	out, errOut, err := runApart(t, "new", "box", "--dir", repo, "--detach", "--json", "--", "sleep", "60")
	var warn struct {
		V       int
		Warning map[string]string
	}
	if err != nil || json.Unmarshal([]byte(errOut), &warn) != nil || warn.Warning["code"] != "setup_skipped" || !json.Valid([]byte(out)) {
		t.Fatalf("new --json: %v\nstdout %s\nstderr %s", err, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), "setup-ran")); err == nil {
		t.Error("the setup script ran")
	}
}

// Each prompt's text, against a golden file (cli-spec 6.4).
func TestGoldenPrompts(t *testing.T) {
	_, tr := withFake(t, nil)
	if _, ok := helpers.For("linux", "arm64"); !ok {
		t.Skip("this build has no helper for linux/arm64 (run just helpers)")
	}
	var b strings.Builder
	show := func(name, in string, ask func(p *prompter) error) {
		var out bytes.Buffer
		err := ask(testPrompts(in, &out))
		fmt.Fprintf(&b, "== %s (answers %q)\n%s\n", name, in, out.String())
		if err != nil {
			fmt.Fprintf(&b, "-> %v\n", err)
		}
	}
	show("new: a missing folder", "\n", func(p *prompter) error {
		_, err := askMakeFolder(p, "box", client.FolderQuestion{Folder: "/home/dev/src/new-app"})
		return err
	})
	show("new: a setup script", "y\n", func(p *prompter) error {
		_, err := askSetup(p, "box", client.SetupQuestion{Repo: "/home/dev/src/app", Script: "/home/dev/src/app/.<name>/setup"}, client.SetupNotTrusted)
		return err
	})
	show("doctor --fix", "y\n", func(p *prompter) error {
		return offerFixes(p, qFix, sampleFixes[:1], false, func([]string) ([]client.Fix, error) { return nil, nil })
	})
	_ = saveHost(context.Background(), client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}})
	show("host rm: created host (Enter keeps machine)", "\n", func(p *prompter) error {
		return rmHost(context.Background(), p, "box", hostRmOptions{})
	})
	_ = saveHost(context.Background(), client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}})
	show("host rm --destroy (Enter cancels)", "\n", func(p *prompter) error {
		return rmHost(context.Background(), p, "box", hostRmOptions{destroy: true})
	})
	show("host destroy", "no\n", func(p *prompter) error { return destroyHost(context.Background(), p, "box", false, false) })
	tr.NoHelper = true
	show("host add: the helper", "n\n", func(p *prompter) error {
		var err error
		// The helper's description goes to stderr, apart from the question.
		captureOutput(t, func() {
			err = addHost(context.Background(), p, client.Host{Name: "new", Machine: &client.Machine{Provider: "fake", ID: "fake-new"}}, false)
		})
		return err
	})
	show("three answers that are not one", "x\nmaybe\n?\n", func(p *prompter) error {
		_, err := p.yesNo("Install it?", false)
		return err
	})
	show("a typed name", "box\n", func(p *prompter) error {
		_, err := p.typedName("Type box to delete the machine, or press Enter to keep it:", "box")
		return err
	})
	running := client.Session{ID: "abcd2345", Name: "auth-fix", Agent: "claude", Cwd: "/home/dev/src/app", State: "working"}
	show("rm", "\n", func(p *prompter) error {
		ok, err := askDelete(p, "box", running)
		if err == nil && ok {
			return errors.New("Enter deleted")
		}
		return err
	})
	ended := client.Session{ID: "abcd2345", Name: "auth-fix", Agent: "claude", Cwd: "/home/dev/src/app", State: "exited", ExitCode: new(0)}
	show("rm: an ended agent", "y\n", func(p *prompter) error {
		_, err := askDelete(p, "box", ended)
		return err
	})
	show("attach: an ended agent", "\n", func(p *prompter) error {
		ok, err := askRestart(p, "box", ended)
		if err == nil && !ok {
			return errors.New("Enter did not start it again")
		}
		return err
	})
	got := strings.ReplaceAll(b.String(), brand.Name, "<name>")
	golden := filepath.Join("testdata", "prompts.golden")
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("the prompts differ from %s (run go test ./internal/cli -run TestGoldenPrompts -update, and read the diff):\n%s", golden, got)
	}
}

// A cancel prints one line, and exits with 130.
func TestCancelPrintsOneLine(t *testing.T) {
	var b bytes.Buffer
	if code := printError(&b, cancelled(), nil, true, false); code != 130 || b.String() != "Cancelled. Nothing changed.\n" {
		t.Errorf("printed %q, exit %d", b.String(), code)
	}
	b.Reset()
	if code := printErrorJSON(&b, cancelled("saved the host box"), nil, false); code != 130 ||
		b.String() != `{"v":1,"error":{"code":"cancelled","kind":"cancelled","message":"Cancelled. Done before that: saved the host box.","exit":130}}`+"\n" {
		t.Errorf("printed %q, exit %d", b.String(), code)
	}
}
