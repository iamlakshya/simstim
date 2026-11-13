package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
)

// The help topics (docs/contributing/cli-spec.md 8.3). A topic is a command
// with only Use, Short and Long: the root help lists it under "Additional
// help topics", and help <topic> prints it. cmd/docgen writes each one to
// docs/reference/<topic>.md, with the same prose.
//
// A topic runs, hidden, so that a word after it fails as an extra word
// does after any other command: Cobra prints the help of a command that
// does not run before it looks at the words. topicMark tells it from a
// command.
const topicMark = "help-topic"

// IsTopic reports whether c is a help topic.
func IsTopic(c *cobra.Command) bool { return c.Annotations[topicMark] != "" }

// Concept is one word that a user meets, and what it means.
type Concept struct {
	Word, Meaning string
}

// concepts are the words of help concepts, one entry each. An entry that is
// wider than a terminal wraps under its meaning. A task that adds a word
// that users meet adds its entry here, in the same change. C5 widens the
// address once every verb takes -.
var concepts = []Concept{
	{"host", "The Linux machine, VM or container that runs sessions"},
	{"helper", "The program on each host that runs its sessions"},
	{"session", "A terminal on a host, which keeps running when you leave"},
	{"address", "<host>/<session>. attach and flip take - for the last one"},
	{"agent", "A coding agent, such as Claude Code or Codex"},
	{"launcher", "The screen of hosts and sessions that " + brand.Name + " opens"},
	{"attach and detach", "Attach shows a session in this terminal. Ctrl+\\ detaches by default, " +
		"and attach.detach_key can change it"},
	{"worktree", "A session's own git worktree and branch (new --worktree)"},
	{"secret", "A value from a command on this computer, such as a token"},
	{"alert", "A message from a host when a session needs you or fails"},
	{"provider script", "A script of yours that starts, stops and checks a host"},
}

// Concepts returns the words of help concepts, for the reference docs.
func Concepts() []Concept { return concepts }

// The prose of the topics, around their tables. The reference pages print
// the same.
const (
	ConceptsIntro  = "The words that " + brand.DisplayName + " uses, and what each one means."
	ExitCodesIntro = "Every command exits with one of these codes. The code comes from the kind of the error, " +
		"never from its message, so a script can rely on it. The error goes to stderr: Error: and the message, " +
		"then one Try: line with the next step. With --json, it is one JSON line on stderr instead."
	ExitCodesOutro = "The shell gives 128 plus the number of the signal that ended a program, so Ctrl+C " +
		"outside an attach gives 130. SIGTERM gives 130 too, because both cancel."
)

// topics are the help topics.
func topics() []*cobra.Command {
	out := []*cobra.Command{
		{Use: "concepts", Short: "The words that this tool uses", Long: conceptsText()},
		{Use: "exit-codes", Short: "What each exit code means", Long: exitCodesText()},
	}
	for _, t := range out {
		t.Hidden = true
		t.Annotations = map[string]string{topicMark: "yes"}
		t.Args = cobra.NoArgs
		t.RunE = func(c *cobra.Command, _ []string) error { return c.Help() }
		t.SetHelpFunc(func(c *cobra.Command, _ []string) { fmt.Fprintln(c.OutOrStdout(), c.Long) })
	}
	return out
}

// paragraph is text wrapped at helpWidth.
func paragraph(text string) string { return strings.Join(wrap(text, helpWidth), "\n") }

// conceptsText is the text of help concepts.
func conceptsText() string {
	rows := make([][]string, len(concepts))
	for i, c := range concepts {
		rows[i] = []string{c.Word, c.Meaning + "."}
	}
	return paragraph(ConceptsIntro) + "\n\n" + strings.TrimRight(columns(rows, "  "), "\n")
}

// exitCodesText is the text of help exit-codes: the table of exit codes,
// the same one that printError reads. It holds the table of section 5.1
// only: P1.13 adds that of 5.2, the codes of exec, with exec.
func exitCodesText() string {
	rows := make([][]string, 0, 1+2*len(exitCodes))
	rows = append(rows, []string{"Code", "Kind", "Meaning"})
	for _, e := range exitCodes {
		kind := ""
		if e.Code != 0 {
			kind = e.Kind.String()
		}
		rows = append(rows, []string{strconv.Itoa(e.Code), kind, e.Meaning + "\n" + e.Examples}, nil)
	}
	return paragraph(ExitCodesIntro) + "\n\n" + columns(rows[:len(rows)-1], "") + "\n" + paragraph(ExitCodesOutro)
}

// columns lays rows out in columns, after indent. Every column but the last
// is as wide as its widest cell, and the last wraps at helpWidth. A newline
// in the last cell starts a new line of it. A nil row is a blank line.
func columns(rows [][]string, indent string) string {
	var widths []int
	for _, r := range rows {
		for i := range len(r) - 1 {
			if i == len(widths) {
				widths = append(widths, 0)
			}
			widths[i] = max(widths[i], len([]rune(r[i])))
		}
	}
	var b strings.Builder
	for _, r := range rows {
		if len(r) == 0 {
			b.WriteString("\n")
			continue
		}
		var lb strings.Builder
		lb.WriteString(indent)
		for i := range len(r) - 1 {
			fmt.Fprintf(&lb, "%-*s  ", widths[i], r[i])
		}
		left := lb.String()
		pad := strings.Repeat(" ", len([]rune(left)))
		first := true
		for _, para := range strings.Split(r[len(r)-1], "\n") {
			for _, l := range wrap(para, helpWidth-len(pad)) {
				if first {
					b.WriteString(strings.TrimRight(left+l, " ") + "\n")
					first = false
				} else {
					b.WriteString(pad + l + "\n")
				}
			}
		}
	}
	return b.String()
}

// wrap breaks text into lines of at most width runes, between words. A
// word longer than width gets a line of its own.
func wrap(text string, width int) []string {
	var lines []string
	line := ""
	for _, w := range strings.Fields(text) {
		switch {
		case line == "":
			line = w
		case len([]rune(line))+1+len([]rune(w)) <= width:
			line += " " + w
		default:
			lines = append(lines, line)
			line = w
		}
	}
	return append(lines, line)
}
