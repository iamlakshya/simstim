package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

func TestExitCodeOfEachKind(t *testing.T) {
	for k, want := range map[client.Kind]int{
		client.KindFailed: 1, client.KindUsage: 2, client.KindNotFound: 3,
		client.KindNeedsAnswer: 4, client.KindRefused: 5, client.KindCancelled: 130,
	} {
		if got := exitCode(k); got != want {
			t.Errorf("exitCode(%v) = %d, want %d", k, got, want)
		}
	}
	seen := map[int]bool{}
	for _, e := range ExitCodes() {
		if seen[e.Code] || e.Meaning == "" || e.Examples == "" {
			t.Errorf("row %+v: a code twice, or no meaning or examples", e)
		}
		seen[e.Code] = true
	}
}

// The code comes from the kind, never from the text.
func TestExitCodeIgnoresTheMessage(t *testing.T) {
	for _, c := range []struct {
		err  error
		want int
	}{
		{&client.UserError{Msg: "no hosts yet", Hint: "h", Code: "no_hosts"}, 1},
		{&client.UserError{Msg: "cannot reach box: connection refused", Hint: "h", Code: "unreachable"}, 1},
		{&client.UserError{Msg: "anything", Hint: "h", Code: "host_not_found", Kind: client.KindNotFound}, 3},
		{client.ErrClosed, 1},
		{errors.New("no session x"), 1},
		{fmt.Errorf("wrapped: %w", &client.UserError{Msg: "m", Hint: "h", Code: "c", Kind: client.KindRefused}), 5},
	} {
		if got := printError(io.Discard, c.err, nil, false, false); got != c.want {
			t.Errorf("printError(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}

// A signal makes a run a cancel, also when the command returned nil.
func TestFinish(t *testing.T) {
	var b bytes.Buffer
	if code := finish(&b, nil, nil, false, false); code != 0 || b.Len() != 0 {
		t.Errorf("a run without error = %d %q", code, b.String())
	}
	if code := finish(&b, nil, nil, false, true); code != 130 || !strings.Contains(b.String(), "Error: the command stopped, because it got a signal to end.") {
		t.Errorf("a signal after no error = %d %q", code, b.String())
	}
	b.Reset()
	ue := &client.UserError{Msg: "setup stopped; nothing was changed", Hint: brand.Name + " setup", Code: "cancelled", Kind: client.KindCancelled}
	if code := finish(&b, errors.New("context canceled"), nil, false, true); code != 130 || !strings.Contains(b.String(), "got a signal") {
		t.Errorf("a signal after a plain error = %d %q", code, b.String())
	}
	b.Reset()
	if code := finish(&b, &client.UserError{Msg: "cannot reach box", Hint: "ssh box", Code: "unreachable"}, nil, false, true); code != 130 || !strings.Contains(b.String(), "cannot reach box.") {
		t.Errorf("a signal keeps a user error's words: %d %q", code, b.String())
	}
	b.Reset()
	if code := finish(&b, ue, nil, false, false); code != 130 || !strings.Contains(b.String(), "setup stopped") {
		t.Errorf("a cancel = %d %q", code, b.String())
	}
}

// Every error gets a Try: line, whatever it came with.
func TestDescribeAlwaysHasATry(t *testing.T) {
	root := Root()
	for _, err := range []error{
		errors.New("plain"), client.ErrClosed, errSignal,
		&client.UserError{Msg: "m", Code: "c"},
		&client.UserError{Msg: "m", Code: "c", Kind: client.KindNotFound},
		&client.UnknownNameError{UserError: &client.UserError{Msg: "m", Code: "c", Kind: client.KindNotFound}, Name: "bx", Known: []string{"box"}},
	} {
		for _, cmd := range []*cobra.Command{nil, root} {
			if r := describe(err, cmd); strings.TrimSpace(r.try) == "" {
				t.Errorf("describe(%v) has no Try: line", err)
			}
		}
	}
}

// What reaches the terminal has no control bytes: not from a host, and not
// from the words of the command line.
func TestErrorsAreCleaned(t *testing.T) {
	var b bytes.Buffer
	printError(&b, &client.UserError{Msg: "no session \x1bc\x1b]52;c;aGk=\x07x", Hint: "Run it\x1b[2J again.", Code: "not_found", Kind: client.KindNotFound}, nil, false, false)
	if bytes.ContainsAny(b.Bytes(), "\x1b\x07") || !strings.Contains(b.String(), "no session c]52;c;aGk=x.") {
		t.Errorf("printed %q", b.String())
	}
	hosttest.New(t)
	got, _ := lineErr(t, "atach", "box/a\x1bcb")
	if strings.Contains(got, "\x1b") || !strings.Contains(got, "Try: "+brand.Name+" attach box/acb\n") {
		t.Errorf("printed %q", got)
	}
}

func TestColourOn(t *testing.T) {
	tty := hosttest.NewTerminal(t, 80, 24).TTY
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	for _, c := range []struct {
		f             *os.File
		noColor, term string
		want          bool
	}{
		{tty, "", "xterm-256color", true},
		{w, "", "xterm-256color", false},
		{tty, "1", "xterm-256color", false},
		{tty, "", "dumb", false},
	} {
		t.Setenv("NO_COLOR", c.noColor)
		t.Setenv("TERM", c.term)
		if got := colourOn(c.f); got != c.want {
			t.Errorf("colourOn(tty %v, NO_COLOR=%q, TERM=%q) = %v", c.f == tty, c.noColor, c.term, got)
		}
	}
}

// line runs the command line as Main does, and returns the command that
// ran and its error.
func line(t *testing.T, args ...string) (*cobra.Command, error) {
	t.Helper()
	root := Root()
	root.SetArgs(args)
	root.SetOut(io.Discard)
	return root.ExecuteContextC(withLine(context.Background(), args))
}

// lineErr runs the command line, and returns the error as printError
// writes it, without colour, and its exit code.
func lineErr(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd, err := line(t, args...)
	if err == nil {
		t.Fatalf("%s: no error", strings.Join(args, " "))
	}
	var b bytes.Buffer
	code := printError(&b, err, cmd, false, false)
	return b.String(), code
}

func TestGoldenErrors(t *testing.T) {
	hosttest.New(t)
	var b strings.Builder
	show := func(name string, err error, cmd *cobra.Command) {
		for _, colour := range []bool{false, true} {
			var out bytes.Buffer
			code := printError(&out, err, cmd, colour, false)
			fmt.Fprintf(&b, "== %s, colour %v, exit %d\n%s", name, colour, code, out.String())
		}
	}
	for _, args := range [][]string{{"atach", "box/auth-fix"}, {"d"}, {"xyz"}, {"host", "ad", "box"}, {"attach"}, {"rename", "a", "b", "c"},
		{"new", "box", "--detatch"}, {"new", "--dir"}, {"debug", "start", "box/s", "--for", "soon"}} {
		cmd, err := line(t, args...)
		show(strings.Join(args, " "), err, cmd)
	}
	show("not found", &client.UserError{Msg: `no host named "bx"`, Hint: brand.Name + " host add bx", Code: "host_not_found", Kind: client.KindNotFound}, nil)
	show("needs an answer", &client.UserError{Msg: "setup asks its questions on a terminal", Hint: brand.Name + " setup --host <host> --yes", Code: "answers_needed", Kind: client.KindNeedsAnswer}, nil)
	show("refused", &client.UserError{Msg: "devbox runs sessions at microvm or stronger, and process is weaker", Hint: brand.Name + " new devbox --wall microvm", Code: "wall_floor", Kind: client.HostKind("wall_floor")}, nil)
	show("cancelled", &client.UserError{Msg: "cancelled; nothing was deleted", Hint: brand.Name + " host destroy box", Code: "cancelled", Kind: client.KindCancelled}, nil)
	show("a host error without a hint", &client.UserError{Msg: "no session abc", Code: "not_found", Kind: client.HostKind("not_found")}, nil)
	show("closed", client.ErrClosed, nil)
	show("not a user error", errors.New("read /home/me/hosts.toml: permission denied"), nil)
	got := strings.ReplaceAll(b.String(), brand.Name, "<name>")
	golden := filepath.Join("testdata", "errors.golden")
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
		t.Errorf("the errors differ from %s (run go test ./internal/cli -run TestGoldenErrors -update, and read the diff):\n%s", golden, got)
	}
}

func TestShellWord(t *testing.T) {
	for in, want := range map[string]string{
		"box/auth-fix": "box/auth-fix", "a b": "'a b'", "it's": `'it'\''s'`, "": "''", "--dir=~/x": "'--dir=~/x'",
		"box/a\x1bcb": "box/acb", "\x1b]52;c;aGk=\x07": "']52;c;aGk='",
	} {
		if got := shellWord(in); got != want {
			t.Errorf("shellWord(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUseArgs(t *testing.T) {
	for use, want := range map[string]string{
		"attach <host>/<session> | -":         "<host>/<session> or -",
		"rename <host>/<session> <new-name>":  "<host>/<session>,<new-name>",
		"bundle [<host> | <host>/<session>]":  "[<host> | <host>/<session>]",
		"new <host> [-- command…]":            "<host>,[-- command…]",
		"ls":                                  "",
		"profile <host>/<session> cpu|heap|x": "<host>/<session>,cpu|heap|x",
	} {
		if got := strings.Join(useArgs(use), ","); got != want {
			t.Errorf("useArgs(%q) = %q, want %q", use, got, want)
		}
	}
}

// Every command that needs a word has an example, which a missing word
// prints (question C-4).
func TestEveryCommandWithAnArgumentHasAnExample(t *testing.T) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			walk(sub)
		}
		if !c.Runnable() || c.Hidden || c.Name() == "help" {
			return
		}
		for _, w := range useArgs(c.Use) {
			if !strings.HasPrefix(w, "[") && firstExample(c.Example) == "" {
				t.Errorf("%s needs %s, and has no example", c.CommandPath(), w)
				return
			}
		}
	}
	walk(Root())
}

func captureStderr(t *testing.T, fn func() int) (string, int) {
	t.Helper()
	t.Setenv("NO_COLOR", "1")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	code := fn()
	os.Stderr = old
	_ = w.Close()
	b, _ := io.ReadAll(r)
	return string(b), code
}
