package cli

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/iamlakshya/simstim/internal/client"
)

// The command line as the user typed it, so that a suggestion can repeat it
// with one word fixed (docs/contributing/cli-spec.md 4.3).

// lineKey holds the command line in the context of a run.
type lineKey struct{}

func withLine(ctx context.Context, args []string) context.Context {
	return context.WithValue(ctx, lineKey{}, args)
}

func commandLine(cmd *cobra.Command) []string {
	if ctx := cmd.Context(); ctx != nil {
		if args, ok := ctx.Value(lineKey{}).([]string); ok {
			return args
		}
	}
	return nil
}

// word is one word of a command line, as the flag parser reads it.
type word struct {
	at int // its index in the line
	// flag is the long name of the flag whose value the word is, or "" for
	// a word that is not a flag's value.
	flag string
	// isFlag is true for the word that names a flag.
	isFlag bool
	// value is where the flag's value starts in the word: after the "=" of
	// --name=value, or 0.
	value int
}

// words reads line with the flags of fs, as pflag does: a flag that takes a
// value takes the next word, unless the value follows an "=", and every
// word after "--" is a plain word.
func words(fs *pflag.FlagSet, line []string) []word {
	var out []word
	rest := false
	for i := 0; i < len(line); i++ {
		w := line[i]
		switch {
		case rest || w == "-" || !strings.HasPrefix(w, "-"):
			out = append(out, word{at: i})
		case w == "--":
			rest = true
		case strings.HasPrefix(w, "--"):
			name, _, inline := strings.Cut(w[2:], "=")
			out = append(out, word{at: i, isFlag: true})
			if f := fs.Lookup(name); inline {
				out = append(out, word{at: i, flag: name, value: 3 + len(name)})
			} else if f != nil && f.NoOptDefVal == "" && i+1 < len(line) {
				i++
				out = append(out, word{at: i, flag: name})
			}
		default:
			out = append(out, word{at: i, isFlag: true})
			for j := 1; j < len(w); j++ {
				f := fs.ShorthandLookup(w[j : j+1])
				if f == nil || f.NoOptDefVal != "" {
					continue
				}
				if j+1 < len(w) {
					out = append(out, word{at: i, flag: f.Name, value: j + 1})
				} else if i+1 < len(line) {
					i++
					out = append(out, word{at: i, flag: f.Name})
				}
				break
			}
		}
	}
	return out
}

// plain returns the indexes in line of the words that are neither flags
// nor their values, as the flags of c read them.
func plain(c *cobra.Command, line []string) []int {
	var out []int
	for _, w := range words(c.Flags(), line) {
		if !w.isFlag && w.flag == "" {
			out = append(out, w.at)
		}
	}
	return out
}

// pathLen is the number of words of c's path after the product's name.
func pathLen(c *cobra.Command) int { return len(strings.Fields(shortPath(c))) }

// replaceAt is line with the word at index i replaced by the words of
// with.
func replaceAt(line []string, i int, with string) []string {
	out := slices.Clone(line[:i])
	out = append(out, strings.Fields(with)...)
	return append(out, line[i+1:]...)
}

// withName is the command line with the unknown name of e replaced by
// with, in the one word where the user typed it for cmd: the value of the
// flag that e names (its last one, which the flag keeps), or else the
// argument at e's index. In that word, the name may be all of it, the host
// of an address such as name/session, or an item of a list such as a,name.
func withName(cmd *cobra.Command, line []string, e *client.UnknownNameError, with string) ([]string, bool) {
	at, head := -1, 0
	skip, arg := pathLen(cmd), 0
	for _, w := range words(cmd.Flags(), line) {
		switch {
		case w.isFlag:
		case e.Flag != "":
			if w.flag == e.Flag {
				at, head = w.at, w.value
			}
		case w.flag != "":
		case skip > 0:
			skip--
		case arg == e.Arg:
			at, arg = w.at, arg+1
		default:
			arg++
		}
	}
	if at < 0 {
		return nil, false
	}
	fixed, ok := fixName(line[at][head:], e.Name, e.Item, with)
	if !ok {
		return nil, false
	}
	out := slices.Clone(line)
	out[at] = line[at][:head] + fixed
	return out, true
}

// fromFlag says that the unknown name of err, if it has one, came from the
// value of the flag named name.
func fromFlag(err error, name string) error {
	var e *client.UnknownNameError
	if errors.As(err, &e) {
		e.Flag = name
	}
	return err
}

// fixName replaces name in text: all of it, the host of an address, or the
// item at index item of a list, also a list of one, which may have spaces
// around it.
func fixName(text, name string, item int, with string) (string, bool) {
	switch {
	case text == name:
		return with, true
	case strings.HasPrefix(text, name+"/"):
		return with + text[len(name):], true
	}
	items := strings.Split(text, ",")
	if item >= 0 && item < len(items) && strings.TrimSpace(items[item]) == name {
		items[item] = strings.Replace(items[item], name, with, 1)
		return strings.Join(items, ","), true
	}
	return "", false
}

// parses reports whether args is a command line that would run: it names
// a real command, with flags that it has and the words that it takes.
func parses(args []string) bool {
	root := Root()
	c, rest, err := root.Find(args)
	if err != nil {
		return false
	}
	c.InitDefaultHelpFlag()
	c.InitDefaultVersionFlag()
	if err := c.ParseFlags(rest); err != nil {
		return false
	}
	return c.ValidateArgs(c.Flags().Args()) == nil
}

// shellWord is w as a word that a shell reads back as w, without the
// control and hidden characters that a terminal would act on. It is quoted
// with single quotes when a shell would split it or read it as something
// else.
func shellWord(w string) string { return client.ShellWord(w) }

func joinWords(line []string) string {
	q := make([]string, len(line))
	for i, w := range line {
		q[i] = shellWord(w)
	}
	return strings.Join(q, " ")
}
