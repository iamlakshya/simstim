package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// ExitCode is one row of the table of exit codes
// (docs/contributing/cli-spec.md 5.1).
type ExitCode struct {
	Code int
	// Kind is the kind of error that exits with Code. The row of code 0
	// has none.
	Kind     client.Kind
	Meaning  string
	Examples string
}

// exitCodes is the one table of exit codes: printError reads it, and so
// does the reference page that cmd/docgen writes.
var exitCodes = []ExitCode{
	{Code: 0, Meaning: "Done.", Examples: "A command did its job. Help printed. An attach ended with a detach, or with the agent's end."},
	{Code: 1, Kind: client.KindFailed, Meaning: "The command could not do its job.",
		Examples: "A host did not answer. The helper failed. No hosts yet. A connection that closed. An error that is not a user error."},
	{Code: 2, Kind: client.KindUsage, Meaning: "The command line or a config file is wrong.",
		Examples: "An unknown command or flag. A missing or extra argument. A bad value. Two flags that conflict. A name that matches two sessions. A `hosts.toml` that does not decode."},
	{Code: 3, Kind: client.KindNotFound, Meaning: "A named thing does not exist.",
		Examples: "No host, session, agent, provider, template or secret of that name."},
	{Code: 4, Kind: client.KindNeedsAnswer, Meaning: "The command needs an answer, and input is off.",
		Examples: "`setup` without a terminal, and without the flags that answer its questions. `new` without a terminal, for a folder that does not exist on the host."},
	{Code: 5, Kind: client.KindRefused, Meaning: "A policy or a tier refuses the action.",
		Examples: "None in this release."},
	{Code: 130, Kind: client.KindCancelled, Meaning: "The user or a signal cancelled.",
		Examples: "The answer no. A question of `host add` or `host destroy` that reads no answer, as in a script. Ctrl+C or SIGTERM outside an attach."},
}

// ExitCodes returns the table of exit codes, for the reference docs.
func ExitCodes() []ExitCode { return exitCodes }

// exitCode is the exit code of an error of kind k.
func exitCode(k client.Kind) int {
	for _, e := range exitCodes {
		if e.Code != 0 && e.Kind == k {
			return e.Code
		}
	}
	return 1
}

// debugHint is the Try: line of an error that is not a user error.
var debugHint = brand.Name + " debug bundle (it writes one file to attach to a bug report)"

// errSignal stands for a command that a signal ended, when the command
// itself returned no error.
var errSignal = errors.New("a signal ended the command")

// usageError is a user error of the command line. It carries what
// printError shows between the message and the Try: line: the candidates
// of "Did you mean", or the short help of a missing argument.
type usageError struct {
	*client.UserError
	suggestions    []string
	usage, example string
}

func (e *usageError) Unwrap() error { return e.UserError }

// report is an error as printError shows it.
type report struct {
	msg, try       string
	kind           client.Kind
	suggestions    []string
	usage, example string
	// user is true for a user error, whose message is a sentence.
	user bool
}

// describe turns err into its report. cmd is the command that ran, if
// any: it gives the --help hint for a non-KindFailed error without a hint,
// and the command line that a suggestion for an unknown name repeats.
func describe(err error, cmd *cobra.Command) report {
	var ue *client.UserError
	switch {
	case errors.As(err, &ue):
		r := report{msg: ue.Msg, try: ue.Hint, kind: ue.Kind, user: true}
		var u *usageError
		if errors.As(err, &u) {
			r.suggestions, r.usage, r.example = u.suggestions, u.usage, u.example
		}
		var un *client.UnknownNameError
		if errors.As(err, &un) && cmd != nil {
			r.suggestions, r.try = suggestName(cmd, un)
		}
		if r.try == "" {
			// An older helper, or a host error that came without a hint.
			r.try = debugHint
			if r.kind != client.KindFailed {
				r.try = helpOf(cmd)
			}
		}
		return r
	case errors.Is(err, client.ErrClosed):
		return report{msg: "the connection to the host closed", try: "Run the command again.", user: true}
	default:
		return report{msg: err.Error(), try: debugHint}
	}
}

// helpOf is the command line that prints the help of cmd.
func helpOf(cmd *cobra.Command) string {
	if cmd == nil {
		return brand.Name + " --help"
	}
	return cmd.CommandPath() + " --help"
}

// print writes the report in the one form of an error, on w
// (docs/contributing/cli-spec.md 4.1).
func (r report) print(w io.Writer, colour bool) {
	red, blue, bold, reset := "\x1b[31m", "\x1b[34m", "\x1b[1m", "\x1b[0m"
	if !colour {
		red, blue, bold, reset = "", "", "", ""
	}
	// Each part passes the cleaner, wherever its text came from. A message
	// keeps its lines, and every other part is one line.
	msg := client.CleanBlock(r.msg)
	if r.user && !strings.HasSuffix(msg, ".") && !strings.HasSuffix(msg, "?") && !strings.HasSuffix(msg, "!") {
		msg += "."
	}
	fmt.Fprintf(w, "%s%sError:%s %s\n", bold, red, reset, msg)
	q := make([]string, len(r.suggestions))
	for i, s := range r.suggestions {
		q[i] = fmt.Sprintf("%q", client.CleanLine(s))
	}
	switch len(q) {
	case 0:
	case 1:
		fmt.Fprintf(w, "\nDid you mean %s?\n", q[0])
	default:
		fmt.Fprintf(w, "\nDid you mean one of these: %s?\n", strings.Join(q, ", "))
	}
	if r.usage != "" {
		fmt.Fprintf(w, "\nUsage:   %s\n", client.CleanLine(r.usage))
		if r.example != "" {
			fmt.Fprintf(w, "Example: %s\n", client.CleanLine(r.example))
		}
	}
	fmt.Fprintf(w, "\n%sTry:%s %s\n", blue, reset, client.CleanLine(r.try))
}

// printError prints err with its next step on w, and returns its exit code.
// cmd is the command that failed, if any. signalled is true when a signal
// ended the command, which makes it a cancel.
func printError(w io.Writer, err error, cmd *cobra.Command, colour, signalled bool) int {
	r := describe(err, cmd)
	if signalled {
		r.kind = client.KindCancelled
		if !r.user {
			r.msg, r.try, r.user = "the command stopped, because it got a signal to end", "Run the command again.", true
		}
	}
	r.print(w, colour)
	return exitCode(r.kind)
}

// finish ends a run: it prints err, if any, and returns the exit code. A
// signal makes the run a cancel, also when the command returned no error.
func finish(w io.Writer, err error, cmd *cobra.Command, colour, signalled bool) int {
	if err == nil {
		if !signalled {
			return 0
		}
		err = errSignal
	}
	return printError(w, err, cmd, colour, signalled)
}

// colourOn reports whether f should get colour: it is a terminal, NO_COLOR
// is not set, and TERM is not dumb.
func colourOn(f *os.File) bool {
	return os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb" && isTerminal(f)
}

// synonyms adds suggestions that are far in spelling but close in meaning
// (docs/contributing/cli-spec.md 4.3). It is keyed by the parent command's
// path without the product's name ("" for the top level), then by the typed
// word. A value is the words that replace the typed word. A synonym that
// matches is the only suggestion. A task that adds a command adds its rows
// here.
var synonyms = map[string]map[string]string{
	"": {
		"list":    "ls",
		"remove":  "rm",
		"del":     "rm",
		"connect": "attach",
		"open":    "attach",
		"create":  "new",
		"run":     "new",
		"install": "agents install",
		"hosts":   "host ls",
	},
	"host": {
		"list": "ls",
	},
}

// named is one thing that a typed word may mean: its name, and every name
// that it answers to, such as a command's aliases.
type named struct {
	name    string
	answers []string
}

// suggest returns at most three of things that typed may mean
// (docs/contributing/cli-spec.md 4.3). Case does not count. A word of one
// or two runes matches only a thing with a name that starts with it, and
// when more than three things match, it gets none. A longer word matches a
// thing with a name at most one edit away, for a word of four runes or
// fewer, or two edits away. With prefix, it also matches a thing with a
// name that starts with it. Each thing counts once, by its name. The
// nearest come first, by the real edit distance, then in alphabetical
// order.
func suggest(typed string, things []named, prefix bool) []string {
	low := strings.ToLower(typed)
	if low == "" {
		return nil
	}
	runes := len([]rune(low))
	short := runes <= 2
	most := 2
	if runes <= 4 {
		most = 1
	}
	type candidate struct {
		name string
		dist int
	}
	var list []candidate
	for _, t := range things {
		best, near := -1, false
		for _, a := range t.answers {
			la := strings.ToLower(a)
			d := distance(low, la)
			if best < 0 || d < best {
				best = d
			}
			starts := strings.HasPrefix(la, low)
			near = near || starts && (short || prefix) || !short && d <= most
		}
		if near {
			list = append(list, candidate{t.name, best})
		}
	}
	if short && len(list) > 3 {
		return nil
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].dist != list[j].dist {
			return list[i].dist < list[j].dist
		}
		return list[i].name < list[j].name
	})
	var out []string
	for i := 0; i < len(list) && i < 3; i++ {
		out = append(out, list[i].name)
	}
	return out
}

// distance is the Levenshtein distance between a and b.
func distance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// suggestName gives the suggestions for an unknown name, such as a host's,
// and the Try: line: the command line with the nearest name in place of
// the typed one, when that line parses. Otherwise the Try: line is the
// error's own hint.
func suggestName(cmd *cobra.Command, e *client.UnknownNameError) ([]string, string) {
	things := make([]named, len(e.Known))
	for i, k := range e.Known {
		things[i] = named{k, []string{k}}
	}
	cands := suggest(e.Name, things, false)
	if len(cands) == 0 {
		return nil, e.Hint
	}
	if line, ok := withName(cmd, commandLine(cmd), e, cands[0]); ok && parses(line) {
		return cands, brand.Name + " " + joinWords(line)
	}
	return cands, e.Hint
}
