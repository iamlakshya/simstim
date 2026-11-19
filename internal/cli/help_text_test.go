package cli

import (
	"regexp"
	"strings"
	"testing"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

// The text of the help: examples, words, placeholders and width
// (docs/contributing/cli-spec.md 8.2, 8.4 and 8.6).

// Every command that runs has an example, so its help and its reference
// page show one. A hidden command's help shows too. A topic runs only to
// print itself, and has none.
func TestEveryCommandHasExample(t *testing.T) {
	eachCommand(Root(), func(c *cobra.Command) {
		if c.Runnable() && !IsTopic(c) && strings.TrimSpace(c.Example) == "" {
			t.Errorf("%s has no example", c.CommandPath())
		}
	})
}

// exampleComment is the comment at the end of an example line.
var exampleComment = regexp.MustCompile(`\s+#\s.*$`)

// exampleLines are the command lines of an example: each line, with the
// lines that a backslash continues joined to it, without its comment, and
// cut to the product's own command where a shell wraps it, as in
// "source <(… completion zsh)" or "… | source".
func exampleLines(ex string) []string {
	var out []string
	cont := ""
	for _, l := range strings.Split(ex, "\n") {
		l = strings.TrimSpace(cont + " " + strings.TrimSpace(l))
		if before, ok := strings.CutSuffix(l, "\\"); ok {
			cont = strings.TrimSpace(before)
			continue
		}
		cont = ""
		if l = exampleComment.ReplaceAllString(l, ""); l == "" {
			continue
		}
		l = strings.ReplaceAll(l, "<(", "")
		l, _, _ = strings.Cut(l, ")")
		l, _, _ = strings.Cut(l, " | ")
		out = append(out, l)
	}
	return out
}

// shellWords splits a line as a shell would, for the quotes that the
// examples use: single quotes keep everything, double quotes keep spaces.
func shellWords(line string) []string {
	var words []string
	var w strings.Builder
	in, quote := false, rune(0)
	for _, r := range line {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote != 0:
			w.WriteRune(r)
		case r == '\'' || r == '"':
			quote, in = r, true
		case unicode.IsSpace(r):
			if in {
				words = append(words, w.String())
				w.Reset()
				in = false
			}
		default:
			w.WriteRune(r)
			in = true
		}
	}
	if in {
		words = append(words, w.String())
	}
	return words
}

// Every example runs as written: it names a command, with flags that the
// command has, the words that it takes, and its required flags.
func TestEveryExampleParses(t *testing.T) {
	n := 0
	eachCommand(Root(), func(c *cobra.Command) {
		for _, line := range exampleLines(c.Example) {
			n++
			words := shellWords(line)
			at := -1
			for i, w := range words {
				if w == brand.Name {
					at = i
					break
				}
			}
			if at < 0 {
				t.Errorf("%s: the example %q runs no %s command", c.CommandPath(), line, brand.Name)
				continue
			}
			if err := exampleParses(words[at+1:]); err != "" {
				t.Errorf("%s: the example %q does not run: %s", c.CommandPath(), line, err)
			}
		}
	})
	if n < 50 {
		t.Errorf("only %d examples", n)
	}
	if got := exampleLines("  a \\\n    b   # c\n  source <(x y)\n  x z | source"); strings.Join(got, ",") != "a b,source x y,x z" {
		t.Errorf("exampleLines = %q", got)
	}
	if got := shellWords(`a "b c" 'd "e"' f`); strings.Join(got, ",") != `a,b c,d "e",f` {
		t.Errorf("shellWords = %q", got)
	}
	// The short help of a missing argument shows the first example whole.
	if got := firstExample("\n  a --x \\\n    --y z\n  b"); got != "a --x --y z" {
		t.Errorf("firstExample = %q", got)
	}
	// A mutant: an example that names a flag the command does not have.
	if exampleParses([]string{"new", "my-vps", "--detatch"}) == "" || exampleParses([]string{"rename", "a/b"}) == "" ||
		exampleParses([]string{"atach", "a/b"}) == "" {
		t.Error("a bad example parses")
	}
}

// exampleParses says why args, a command line without the product's
// name, would not run, or "".
func exampleParses(args []string) string {
	c, rest, err := Root().Find(args)
	if err != nil {
		return err.Error()
	}
	if !c.Runnable() {
		return c.CommandPath() + " does not run"
	}
	c.InitDefaultHelpFlag()
	c.InitDefaultVersionFlag()
	if err := c.ParseFlags(rest); err != nil {
		return err.Error()
	}
	if err := c.ValidateArgs(c.Flags().Args()); err != nil {
		return err.Error()
	}
	if err := c.ValidateRequiredFlags(); err != nil {
		return err.Error()
	}
	if strings.Contains(c.Use, "<host>/<session>") {
		args := c.Flags().Args()
		hostChoice := strings.Contains(c.Use, "<host>|") || strings.Contains(c.Use, "|<host>")
		if len(args) > 0 && args[0] != "-" && args[0] != "my-vps/auth-fix" && (!hostChoice || args[0] != "my-vps") {
			return "a session example must use my-vps/auth-fix"
		}
	}
	return ""
}

// A command's short help is one line with no full stop at its end.
func TestShortIsOneLine(t *testing.T) {
	eachCommand(Root(), func(c *cobra.Command) {
		if s := c.Short; s == "" || strings.HasSuffix(s, ".") || strings.Contains(s, "\n") {
			t.Errorf("%s: its short help %q is empty, ends with a full stop, or has two lines", c.CommandPath(), s)
		}
	})
}

// internalWords are the words of the build that a user does not know
// (docs/contributing/cli-spec.md 8.4), in any case, with any space, line
// break or dash inside, and in every form. "placeholder" holds no word.
var internalWords = regexp.MustCompile(`(?i)\b(holders?|flight[\s-]*recorders?|linger(s|ed|ing)?|restore[\s-]*units?)\b`)

// internalWordsIn lists the internal words in the help of c: its Use,
// Short, Long, Example and Deprecated, and its flags' usage and
// deprecation.
func internalWordsIn(c *cobra.Command) []string {
	texts := map[string]string{"Use": c.Use, "Short": c.Short, "Long": c.Long, "Example": c.Example, "Deprecated": c.Deprecated}
	for _, fs := range []*pflag.FlagSet{c.LocalFlags(), c.PersistentFlags(), c.InheritedFlags()} {
		fs.VisitAll(func(f *pflag.Flag) {
			texts["--"+f.Name] = f.Usage + "\n" + f.Deprecated + "\n" + f.ShorthandDeprecated
		})
	}
	var out []string
	for part, text := range texts {
		for _, w := range internalWords.FindAllString(text, -1) {
			out = append(out, part+": "+w)
		}
	}
	return out
}

func TestHelpHasNoInternalWords(t *testing.T) {
	eachCommand(Root(), func(c *cobra.Command) {
		for _, w := range internalWordsIn(c) {
			t.Errorf("%s: its %q is a word that users do not know", c.CommandPath(), w)
		}
	})
	for _, ok := range []string{"a placeholder", "stakeholders", "the restore of a unit", "flight", "lingo"} {
		if internalWords.MatchString(ok) {
			t.Errorf("%q matched", ok)
		}
	}
	for _, bad := range []string{"its Holder", "the flight recorder", "the flight\nrecorder", "flight  recorders", "flight-recorder",
		"turn on linger", "sessions lingered", "Lingering", "the restore unit", "the restore\n\tunits"} {
		if !internalWords.MatchString(bad) {
			t.Errorf("%q did not match", bad)
		}
	}
	// A mutant: a Long that a hard wrap breaks in the middle of a word, a
	// hidden command, and a flag's deprecation.
	mutant := &cobra.Command{Use: "save", Hidden: true, Long: "Saves the session's flight\nrecorder."}
	mutant.Flags().Bool("all", false, "every one")
	_ = mutant.Flags().MarkDeprecated("all", "the restore unit does it")
	if got := internalWordsIn(mutant); len(got) != 2 {
		t.Errorf("the mutant's words: %q", got)
	}
}

// Each argument in Use has a <placeholder>, choices are a|b outside angle
// brackets, and <name> appears only where a command names a new thing:
// a new host, or a session's new name (docs/contributing/cli-spec.md 8.2).
func TestUsePlaceholders(t *testing.T) {
	newName := map[string]bool{"host add": true, "host create": true, "rename": true}
	angleChoice := regexp.MustCompile(`<[^>]*\|[^>]*>`)
	eachCommand(Root(), func(c *cobra.Command) {
		if strings.Contains(c.Use, "<name>") && !newName[shortPath(c)] {
			t.Errorf("%s uses <name> in %q, which is only for a new name", c.CommandPath(), c.Use)
		}
		if angleChoice.MatchString(c.Use) {
			t.Errorf("%s puts | inside <…> in %q: write <a|b> as <a>|b", c.CommandPath(), c.Use)
		}
		if strings.Contains(c.Use, " |") || strings.Contains(c.Use, "| ") {
			t.Errorf("%s has spaces around | in %q: write a|b", c.CommandPath(), c.Use)
		}
		for _, w := range useArgs(c.Use) {
			if strings.HasPrefix(w, "[") && !strings.Contains(w, "<") {
				t.Errorf("%s has no <placeholder> in optional %q of %q", c.CommandPath(), w, c.Use)
			}
		}
	})
}

// Every command's help fits a terminal of helpWidth columns, the root's
// and the topics' too.
func TestHelpFitsTheWidth(t *testing.T) {
	hosttest.New(t)
	eachCommand(Root(), func(c *cobra.Command) {
		out := helpOutput(t, append(strings.Fields(shortPath(c)), "--help")...)
		for _, l := range strings.Split(out, "\n") {
			if n := displayed(l); n > helpWidth {
				t.Errorf("%s --help: a line of %d columns: %q", c.CommandPath(), n, l)
			}
			if strings.TrimRightFunc(l, unicode.IsSpace) != l {
				t.Errorf("%s --help: a line that ends with a space: %q", c.CommandPath(), l)
			}
		}
	})
	// The regression: Cobra's help of completion zsh indents a command with
	// a tab, which takes it to 83 columns. The help indents it with two
	// spaces.
	zsh := brand.Name + " completion zsh > $(brew --prefix)/share/zsh/site-functions/_" + brand.Name
	if n := displayed("\t" + zsh); n != 8+len(zsh) || n <= helpWidth {
		t.Errorf("the tab before the command counts %d columns", n)
	}
	if out := helpOutput(t, "completion", "zsh", "--help"); !strings.Contains(out, "\n  "+zsh+"\n") {
		t.Errorf("completion zsh --help does not indent the command with two spaces:\n%s", out)
	}
}

// displayed is the width of a line on a terminal: a tab goes on to the
// next multiple of 8, and a wide character takes two columns.
func displayed(line string) int {
	n := 0
	for i, part := range strings.Split(line, "\t") {
		if i > 0 {
			n = (n/8 + 1) * 8
		}
		n += ansi.StringWidth(part)
	}
	return n
}

func TestDisplayed(t *testing.T) {
	for s, want := range map[string]int{"": 0, "abc": 3, "\t": 8, "a\tb": 9, "1234567\tx": 9, "12345678\tx": 17, "日本": 4, "…": 1} {
		if got := displayed(s); got != want {
			t.Errorf("displayed(%q) = %d, want %d", s, got, want)
		}
	}
}

// A topic takes no words, as help <topic> does not: an extra word fails
// with exit code 2.
func TestTopicsTakeNoWords(t *testing.T) {
	hosttest.New(t)
	for _, args := range [][]string{{"concepts", "extra"}, {"help", "concepts", "extra"}, {"exit-codes", "x"}} {
		if got, code := lineErr(t, args...); code != 2 {
			t.Errorf("%s exits with %d:\n%s", strings.Join(args, " "), code, got)
		}
	}
	if out := helpOutput(t, "concepts"); out != helpOutput(t, "help", "concepts") || !strings.HasPrefix(out, paragraph(ConceptsIntro)) {
		t.Errorf("concepts alone printed:\n%s", out)
	}
}

// A topic's prose, around its table, is the prose of its reference page.
func TestTopicProseIsShared(t *testing.T) {
	flat := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	for _, c := range []struct{ topic, prose string }{
		{conceptsText(), ConceptsIntro},
		{exitCodesText(), ExitCodesIntro},
		{exitCodesText(), ExitCodesOutro},
	} {
		if !strings.Contains(flat(c.topic), flat(c.prose)) {
			t.Errorf("the topic does not hold %q:\n%s", c.prose, c.topic)
		}
	}
}

// The address line of help concepts is true: the commands that it names
// take -, and the others do not yet. C5 widens the line.
func TestAddressConceptIsTrue(t *testing.T) {
	hosttest.New(t)
	var line string
	for _, c := range Concepts() {
		if c.Word == "address" {
			line = c.Meaning
		}
	}
	for _, name := range []string{"attach", "flip"} {
		if !strings.Contains(line, name) {
			t.Errorf("the address line does not name %s: %q", name, line)
		}
		if got, code := lineErr(t, name, "-"); code != 3 || !strings.Contains(got, "no session has been attached yet") {
			t.Errorf("%s - exits with %d:\n%s", name, code, got)
		}
	}
	for _, args := range [][]string{{"kill", "-"}, {"ports", "-"}, {"debug", "start", "-"}} {
		if got, code := lineErr(t, args...); code != 2 || !strings.Contains(got, `"-" is not`) {
			t.Errorf("%s takes -, so the address line of help concepts may name it: exit %d\n%s", strings.Join(args, " "), code, got)
		}
	}
}
