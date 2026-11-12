package cli

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// The framework's own errors, in the form of every other error
// (docs/contributing/cli-spec.md 4.4).

// guard gives the tree under root the errors of section 4.4: a noun, a
// command that only holds others, prints its help alone and calls any
// other word an unknown command. Every other command's argument check
// fails with a usage error, and so does every flag.
func guard(root *cobra.Command) {
	root.SetFlagErrorFunc(flagError)
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		switch {
		case c.HasSubCommands() && !c.Runnable():
			c.Args = unknownCommand
			c.RunE = func(c *cobra.Command, _ []string) error { return c.Help() }
		case c.Args != nil:
			c.Args = usageArgs(c.Args)
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
}

// unknownCommand is the argument check of a command that takes no words
// but the names of its subcommands: any other word is an unknown command,
// with suggestions.
func unknownCommand(c *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	return unknownWord(c, args[0], c, pathLen(c))
}

// helpArgs is the argument check of help: its words must name a command,
// or help fails as the command line itself would.
func helpArgs(c *cobra.Command, args []string) error {
	at := c.Root()
	for i, w := range args {
		next := subcommand(at, w)
		if next == nil {
			// Every word that is left, "-" and the words after "--" too,
			// must name a command. It comes after help and i others.
			return unknownWord(at, w, c, 1+i)
		}
		at = next
	}
	return nil
}

// subcommand is the subcommand of c that word names, by its name or an
// alias, or nil.
func subcommand(c *cobra.Command, word string) *cobra.Command {
	for _, sub := range c.Commands() {
		if sub.Name() == word || sub.HasAlias(word) {
			return sub
		}
	}
	return nil
}

// unknownWord is the error of typed, a word that names no command under
// parent. lineCmd is the command whose flags read the command line, and
// skip the number of its plain words before typed.
func unknownWord(parent *cobra.Command, typed string, lineCmd *cobra.Command, skip int) error {
	name := shortPath(parent)
	var cands []string
	if s, ok := synonyms[name][strings.ToLower(typed)]; ok {
		cands = []string{s}
	} else {
		var things []named
		for _, sub := range parent.Commands() {
			if sub.IsAvailableCommand() {
				things = append(things, named{sub.Name(), append([]string{sub.Name()}, sub.Aliases...)})
			}
		}
		cands = suggest(typed, things, true)
	}
	msg := fmt.Sprintf("unknown command %q", typed)
	if name != "" {
		msg = fmt.Sprintf("%s has no command %q", name, typed)
	}
	return &usageError{
		UserError:   &client.UserError{Msg: msg, Hint: commandTry(parent, lineCmd, skip, cands), Code: "unknown_command", Kind: client.KindUsage},
		suggestions: cands,
	}
}

// commandTry is the Try: line of an unknown command under parent. It is
// the command line with the nearest candidate in place of the typed word,
// the plain word of lineCmd's line after skip others, when that line
// parses. Otherwise it is the help of the nearest candidate, or with none,
// the help of parent.
func commandTry(parent, lineCmd *cobra.Command, skip int, cands []string) string {
	if len(cands) == 0 {
		return helpOf(parent)
	}
	line := commandLine(lineCmd)
	if at := plain(lineCmd, line); len(at) > skip {
		if fixed := replaceAt(line, at[skip], cands[0]); parses(fixed) {
			return brand.Name + " " + joinWords(fixed)
		}
	}
	return strings.TrimSuffix(parent.CommandPath()+" "+cands[0], " ") + " --help"
}

// shortPath is a command's path without the product's name: "host add".
func shortPath(c *cobra.Command) string {
	return strings.TrimSpace(strings.TrimPrefix(c.CommandPath(), c.Root().Name()))
}

// usageArgs makes the argument check of a command fail with a usage error.
func usageArgs(check cobra.PositionalArgs) cobra.PositionalArgs {
	return func(c *cobra.Command, args []string) error {
		err := check(c, args)
		var ue *client.UserError
		if err == nil || errors.As(err, &ue) {
			return err
		}
		return argsError(c, args)
	}
}

// argsError is the error of a command line with too few or too many
// words. A missing word gets the command's usage and its first example.
func argsError(c *cobra.Command, args []string) error {
	name, want := shortPath(c), useArgs(c.Use)
	required := 0
	for _, a := range want {
		if !strings.HasPrefix(a, "[") {
			required++
		}
	}
	if len(args) < required {
		return &usageError{
			UserError: &client.UserError{Msg: name + " needs " + strings.Join(want[len(args):required], " "),
				Hint: helpOf(c), Code: "missing_argument", Kind: client.KindUsage},
			usage:   strings.TrimSuffix(c.UseLine(), " [flags]"),
			example: firstExample(c.Example),
		}
	}
	takes := "no words"
	if len(want) > 0 {
		takes = strings.Join(want, " ")
	}
	words := "words"
	if len(args) == 1 {
		words = "word"
	}
	return &client.UserError{Msg: fmt.Sprintf("%s takes %s, and got %d %s", name, takes, len(args), words),
		Hint: helpOf(c), Code: "extra_argument", Kind: client.KindUsage}
}

// useArgs splits the arguments of a command's Use into the words a user
// types: "rename <host>/<session> <new-name>" gives "<host>/<session>" and
// "<new-name>". An optional part keeps its brackets, and choices, as in
// "<host>/<session> | -", join with "or".
func useArgs(use string) []string {
	_, rest, _ := strings.Cut(use, " ")
	var words []string
	depth, from := 0, 0
	for i, r := range rest + " " {
		switch {
		case r == '[':
			depth++
		case r == ']':
			depth--
		case r == ' ' && depth == 0:
			if i > from {
				words = append(words, rest[from:i])
			}
			from = i + 1
		}
	}
	var out []string
	for i := 0; i < len(words); i++ {
		choices := []string{words[i]}
		for i+2 < len(words) && words[i+1] == "|" {
			choices = append(choices, words[i+2])
			i += 2
		}
		out = append(out, strings.Join(choices, " or "))
	}
	return out
}

// firstExample is the first line of a command's examples, without its
// indent.
func firstExample(ex string) string {
	for _, l := range strings.Split(ex, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

// flagError turns the flag parser's errors into usage errors. A word that
// is not a command, before a flag that is not the parent's, is an unknown
// command.
func flagError(c *cobra.Command, err error) error {
	if args := c.Flags().Args(); len(args) > 0 && c.HasAvailableSubCommands() {
		if aerr := c.ValidateArgs(args); aerr != nil {
			return aerr //nolint:wrapcheck // the check is guard's own, and returns a UserError
		}
	}
	name := shortPath(c)
	if name == "" {
		name = brand.Name
	}
	var (
		notExist *pflag.NotExistError
		noValue  *pflag.ValueRequiredError
		badValue *pflag.InvalidValueError
		syntax   *pflag.InvalidSyntaxError
	)
	switch {
	case errors.As(err, &notExist):
		return unknownFlag(c, name, notExist)
	case errors.As(err, &noValue):
		return &client.UserError{Msg: flagName(noValue.GetFlag()) + " needs a value", Hint: helpOf(c), Code: "flag_value", Kind: client.KindUsage}
	case errors.As(err, &badValue):
		return &client.UserError{Msg: fmt.Sprintf("%s %q is not %s", flagName(badValue.GetFlag()), badValue.GetValue(), valueKind(badValue)),
			Hint: helpOf(c), Code: "flag_value", Kind: client.KindUsage}
	case errors.As(err, &syntax):
		return &client.UserError{Msg: fmt.Sprintf("%q is not a flag", syntax.GetSpecifiedFlag()), Hint: helpOf(c), Code: "bad_flag", Kind: client.KindUsage}
	}
	return &client.UserError{Msg: err.Error(), Hint: helpOf(c), Code: "bad_flag", Kind: client.KindUsage}
}

// unknownFlag is the error of a flag that the command does not have, with
// the flags it may mean. The Try: line replaces the flag where the user
// typed it, when the line then parses.
func unknownFlag(c *cobra.Command, name string, e *pflag.NotExistError) error {
	typed := "--" + e.GetSpecifiedName()
	if e.GetSpecifiedShortnames() != "" {
		typed = "-" + e.GetSpecifiedName()
	}
	var cands []string
	if e.GetSpecifiedShortnames() == "" {
		var things []named
		c.Flags().VisitAll(func(f *pflag.Flag) {
			if !f.Hidden && f.Deprecated == "" {
				things = append(things, named{"--" + f.Name, []string{f.Name}})
			}
		})
		cands = suggest(e.GetSpecifiedName(), things, false)
	}
	try := helpOf(c)
	if len(cands) > 0 {
		line := commandLine(c)
		for _, w := range words(c.Flags(), line) {
			if text := line[w.at]; w.isFlag && (text == typed || strings.HasPrefix(text, typed+"=")) {
				fixed := slices.Clone(line)
				fixed[w.at] = cands[0] + strings.TrimPrefix(text, typed)
				if parses(fixed) {
					try = brand.Name + " " + joinWords(fixed)
				}
				break
			}
		}
	}
	return &usageError{
		UserError:   &client.UserError{Msg: fmt.Sprintf("%s has no flag %s", name, typed), Hint: try, Code: "unknown_flag", Kind: client.KindUsage},
		suggestions: cands,
	}
}

func flagName(f *pflag.Flag) string { return "--" + f.Name }

// valueKind says what a flag's value must be, from its type.
func valueKind(e *pflag.InvalidValueError) string {
	switch e.GetFlag().Value.Type() {
	case "duration":
		return "a time, such as 30s or 5m"
	case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "count":
		return "a whole number"
	case "float32", "float64":
		return "a number"
	case "bool":
		return "true or false"
	default:
		return "valid"
	}
}
