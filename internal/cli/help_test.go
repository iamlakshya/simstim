package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
)

// eachCommand calls fn for every command under root, root included, hidden
// ones too.
func eachCommand(root *cobra.Command, fn func(c *cobra.Command)) {
	fn(root)
	for _, sub := range root.Commands() {
		eachCommand(sub, fn)
	}
}

// depthExceptions are the commands whose subcommands sit at the third
// level (docs/contributing/cli-spec.md 2.2, question C-12).
var depthExceptions = map[string]bool{"host alerts": true}

// No command is deeper than two levels, except the subcommands of
// depthExceptions.
func TestDepth(t *testing.T) {
	eachCommand(Root(), func(c *cobra.Command) {
		if depth := len(strings.Fields(shortPath(c))); depth > 2 && !depthExceptions[shortPath(c.Parent())] {
			t.Errorf("%s is %d levels deep, and the most is two", c.CommandPath(), depth)
		}
	})
	root := Root()
	for path := range depthExceptions {
		if c, _, err := root.Find(strings.Fields(path)); err != nil || shortPath(c) != path || !c.HasSubCommands() {
			t.Errorf("the exception %q has no subcommands: remove it from depthExceptions", path)
		}
	}
}

// Every command at the top level is in one group of the root help, and
// every group names commands that exist.
func TestEveryCommandIsInAGroup(t *testing.T) {
	root := Root()
	seen := map[string]bool{}
	for _, g := range HelpGroups() {
		if !root.ContainsGroup(g.ID) || len(g.Commands) == 0 {
			t.Errorf("the group %q is not the root's, or is empty", g.Title)
		}
		for _, name := range g.Commands {
			if c := subcommand(root, name); c == nil || c.GroupID != g.ID || seen[name] {
				t.Errorf("the group %q names %q, which is no command, or is in another group too", g.Title, name)
			}
			seen[name] = true
		}
	}
	for _, c := range root.Commands() {
		if (c.IsAvailableCommand() || c.Name() == "help") && (!root.ContainsGroup(c.GroupID) || !seen[c.Name()]) {
			t.Errorf("%s is in no group of the root help: add it to helpGroups", c.Name())
		}
	}
}

// helpOutput runs the command line and returns what it printed on stdout.
func helpOutput(t *testing.T, args ...string) string {
	t.Helper()
	root := Root()
	var b bytes.Buffer
	root.SetArgs(args)
	root.SetOut(&b)
	if _, err := root.ExecuteContextC(withLine(context.Background(), args)); err != nil {
		t.Fatalf("%s: %v", strings.Join(args, " "), err)
	}
	return b.String()
}

func TestGoldenHelp(t *testing.T) {
	newTestHome(t)
	var b strings.Builder
	for _, args := range [][]string{{"--help"}, {"help", "concepts"}} {
		b.WriteString("== " + strings.Join(args, " ") + "\n" + helpOutput(t, args...))
	}
	got := strings.NewReplacer(brand.Name, "<name>", strings.ToUpper(brand.Name), "<NAME>").Replace(b.String())
	golden := filepath.Join("testdata", "help.golden")
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
		t.Errorf("the help differs from %s (run go test ./internal/cli -run TestGoldenHelp -update, and read the diff):\n%s", golden, got)
	}
}

// The root help lists its groups in order, each with its commands in the
// order of helpGroups, and its topics by name.
func TestRootHelpGroups(t *testing.T) {
	newTestHome(t)
	out := helpOutput(t, "--help")
	at := 0
	for _, g := range HelpGroups() {
		i := strings.Index(out[at:], "\n"+g.Title+":\n")
		if i < 0 {
			t.Fatalf("no group %q after byte %d:\n%s", g.Title, at, out)
		}
		at += i
		for _, name := range g.Commands {
			j := strings.Index(out[at:], "\n  "+name+" ")
			if j < 0 {
				t.Fatalf("%s is not in %q, in order:\n%s", name, g.Title, out)
			}
			at += j
		}
	}
	for _, topic := range []string{"concepts", "exit-codes"} {
		if !strings.Contains(out, "\nAdditional help topics:\n") || !regexp.MustCompile(`\n  `+topic+` +\S`).MatchString(out) {
			t.Errorf("the topic %s is not listed:\n%s", topic, out)
		}
	}
	if !strings.Contains(out, "  "+brand.Name+" ") || !strings.Contains(out, "opens the launcher\n") {
		t.Errorf("the usage does not say what the bare command does:\n%s", out)
	}
	// A command's help is still Cobra's own.
	if sub := helpOutput(t, "host", "--help"); !strings.Contains(sub, "Available Commands:\n  add ") {
		t.Errorf("host --help:\n%s", sub)
	}
}

// help exit-codes prints the table that printError reads, and the topics
// also print as commands of their own.
func TestHelpExitCodes(t *testing.T) {
	newTestHome(t)
	out := helpOutput(t, "help", "exit-codes")
	for _, e := range ExitCodes() {
		line := regexp.MustCompile(`(?m)^` + strconv.Itoa(e.Code) + ` +` + regexp.QuoteMeta(e.Kind.String()+" "))
		if e.Code == 0 {
			line = regexp.MustCompile(`(?m)^0 +` + regexp.QuoteMeta(e.Meaning) + `$`)
		}
		if !line.MatchString(out) || !strings.Contains(out, e.Meaning) {
			t.Errorf("code %d is not in the table:\n%s", e.Code, out)
		}
	}
	for _, l := range strings.Split(out, "\n") {
		if displayed(l) > helpWidth {
			t.Errorf("a line is wider than %d: %q", helpWidth, l)
		}
	}
	if alone := helpOutput(t, "exit-codes"); alone != out {
		t.Errorf("exit-codes alone printed:\n%s", alone)
	}
	// A mistyped topic gets a suggestion, as a mistyped command does.
	if got, code := lineErr(t, "help", "exit-code"); code != 2 || !strings.Contains(got, `Did you mean "exit-codes"?`) ||
		!strings.Contains(got, "Try: "+brand.Name+" help exit-codes\n") {
		t.Errorf("help exit-code exits with %d:\n%s", code, got)
	}
}

// Each word of help concepts starts a line, and its meaning follows it
// whole: on that line, or wrapped under it at the width of a terminal.
func TestConceptsFitTheWidth(t *testing.T) {
	out := conceptsText()
	for _, c := range Concepts() {
		words := strings.Fields(c.Meaning + ".")
		entry := `(?m)^  ` + regexp.QuoteMeta(c.Word) + ` +`
		for i, w := range words {
			if i > 0 {
				entry += `\s+`
			}
			entry += regexp.QuoteMeta(w)
		}
		if !regexp.MustCompile(entry + `$`).MatchString(out) {
			t.Errorf("the entry of %q is not whole:\n%s", c.Word, out)
		}
	}
	for _, l := range strings.Split(out, "\n") {
		if displayed(l) > helpWidth {
			t.Errorf("a line is wider than %d: %q", helpWidth, l)
		}
	}
	// The detach key is a setting: the entry says so (V2).
	if !strings.Contains(strings.Join(strings.Fields(out), " "), `Ctrl+\ detaches by default, and attach.detach_key can change it.`) {
		t.Errorf("the entry of attach and detach does not say that its key can change:\n%s", out)
	}
}

func TestWrap(t *testing.T) {
	for _, c := range []struct {
		text  string
		width int
		want  string
	}{
		{"", 10, ""},
		{"one two three", 7, "one two|three"},
		{"one two three", 13, "one two three"},
		{"a verylongword b", 4, "a|verylongword|b"},
		{"  spaced   words ", 20, "spaced words"},
	} {
		if got := strings.Join(wrap(c.text, c.width), "|"); got != c.want {
			t.Errorf("wrap(%q, %d) = %q, want %q", c.text, c.width, got, c.want)
		}
	}
	got := columns([][]string{{"a", "bb", "one two three"}, nil, {"ccc", "", "x\ny"}}, "> ")
	want := "> a    bb  one two three\n\n> ccc      x\n           y\n"
	if got != want {
		t.Errorf("columns = %q, want %q", got, want)
	}
}

// The help and completion commands that Cobra adds teach as every other
// command does.
func TestAddedCommandsTeach(t *testing.T) {
	for _, c := range Root().Commands() {
		switch c.Name() {
		case "help":
			if c.Long != "" || strings.HasSuffix(c.Short, ".") || !strings.Contains(c.Example, "help concepts") {
				t.Errorf("help: %q %q %q", c.Short, c.Long, c.Example)
			}
		case "completion":
			for _, sh := range c.Commands() {
				if !strings.Contains(sh.Example, "completion "+sh.Name()) {
					t.Errorf("completion %s: %q", sh.Name(), sh.Example)
				}
			}
		}
	}
}
