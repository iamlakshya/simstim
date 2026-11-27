package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/helpers"
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
		// A row's flags may be more than one, with their values, as in
		// "--destroy --confirm <host>".
		for _, f := range strings.Fields(q.flag) {
			switch {
			case strings.HasPrefix(f, "--"):
				if cmd.Flags().Lookup(strings.TrimPrefix(f, "--")) == nil {
					t.Errorf("%s: no flag %s", q.cmd, f)
				}
			case strings.HasPrefix(f, "-"):
				if cmd.Flags().ShorthandLookup(strings.TrimPrefix(f, "-")) == nil {
					t.Errorf("%s: no flag %s", q.cmd, f)
				}
			}
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
			inputOff := map[bool]string{true: "Keep", false: "Fail"}[q.keep]
			if q.refused {
				inputOff = "Refuse"
			}
			if names && strings.Contains(cells[4], q.flag) && strings.Contains(cells[5], inputOff) {
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
		if q.refused {
			if ok || !errors.As(err, &ue) || ue.Kind != client.KindRefused || exitCode(ue.Kind) != 5 || ue.Hint != "Run the command in a terminal, with input on." {
				t.Errorf("%s: %+v, want a terminal-only refusal with its exact hint", q.cmd, err)
			}
			continue
		}
		if !errors.As(err, &ue) || ue.Kind != client.KindNeedsAnswer || exitCode(ue.Kind) != 4 || !strings.HasSuffix(ue.Hint, " "+q.flag) {
			t.Errorf("%s: %+v, want exit code 4 and %s", q.cmd, err, q.flag)
		}
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

func TestLocalCommandsWithNoInput(t *testing.T) {
	newTestHome(t)
	// doctor --fix: each fix needs -y.
	_ = os.MkdirAll(filepath.Join(client.ConfigDir(), "ssh"), 0o700)
	_ = os.WriteFile(filepath.Join(client.ConfigDir(), "ssh", "config-gone"), generatedSSHFixture(), 0o600)
	got, code := lineErr(t, "doctor", "--fix", "--no-input")
	if code != 4 || !strings.Contains(got, "doctor needs a yes for each fix, and input is off") || !strings.Contains(got, "Try: "+brand.Name+" doctor --fix --no-input -y") {
		t.Errorf("doctor --fix: exit %d\n%s", code, got)
	}
	if _, err := os.Stat(filepath.Join(client.ConfigDir(), "ssh", "config-gone")); err != nil {
		t.Error("doctor --fix with input off removed the file")
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
