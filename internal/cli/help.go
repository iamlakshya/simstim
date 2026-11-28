package cli

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
)

// Help that teaches (docs/contributing/cli-spec.md 8): the groups of the
// root help, its usage, and the help of the commands that Cobra adds.

// HelpGroup is one group of the root help: its title, and the names of its
// commands in the order that the help lists them.
type HelpGroup struct {
	ID, Title string
	Commands  []string
}

// helpGroups are the root help's groups (docs/contributing/cli-spec.md 2.1
// and 8.1). Every command at the top level is in one. A task that adds a
// command adds it here. P1.8 adds Policy with its first command, because an
// empty group would print its title.
var helpGroups = []HelpGroup{
	{ID: "sessions", Title: "Sessions", Commands: []string{"new", "attach", "resume", "stop", "ls", "kill", "rename", "send", "ports", "forward", "pull", "cp", "flip"}},
	{ID: "hosts", Title: "Hosts", Commands: []string{"host"}},
	{ID: "agents", Title: "Agents", Commands: []string{"agents"}},
	{ID: "images", Title: "Images", Commands: []string{"image"}},
	{ID: "setup", Title: "Setup and help", Commands: []string{"setup", "doctor", "debug", "config", "ssh-config", "demo", "version", "completion", "help"}},
}

// HelpGroups returns the root help's groups, for the reference docs.
func HelpGroups() []HelpGroup { return helpGroups }

// helpWidth is the width that the help fits in: a line of any command's
// help, or of a topic, is at most this many columns.
const helpWidth = 80

// opensTheLauncher says what the command does alone. The usage line of the
// root help says it, and so does the root's Long, which the reference index
// and the man page print.
const opensTheLauncher = "opens the launcher"

// rootLong is the root's Long. The root help leaves it out, because its
// usage line says the same.
const rootLong = "With no command, " + brand.Name + " " + opensTheLauncher + ": one screen of your hosts and their sessions."

// rootExample is the root's example, which its help shows after the
// usage: each command, with what it does in a comment.
func rootExample() string {
	lines := [][2]string{
		{"setup", "connect a host, step by step"},
		{"new my-vps --agent claude", "start an agent, and attach to it"},
		{"attach my-vps/auth-fix", "go back to a session"},
	}
	width := 0
	for _, l := range lines {
		width = max(width, len(brand.Name+" "+l[0]))
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = fmt.Sprintf("  %-*s   # %s", width, brand.Name+" "+l[0], l[1])
	}
	return strings.Join(out, "\n")
}

// teach gives the root its help: the groups, the usage that lists them,
// and the help of the help and completion commands, which Cobra adds. It
// runs after the root has those two. Every help fits helpWidth.
func teach(root *cobra.Command) {
	for _, g := range helpGroups {
		root.AddGroup(&cobra.Group{ID: g.ID, Title: g.Title + ":"})
		for _, name := range g.Commands {
			if c := subcommand(root, name); c != nil {
				c.GroupID = g.ID
			}
		}
	}
	root.SetHelpCommandGroupID("setup")
	root.SetCompletionCommandGroupID("setup")
	for _, c := range root.Commands() {
		switch c.Name() {
		case "help":
			c.Use = "help [<command>]"
			c.Short = "Show the help of a command or a topic"
			c.Long = ""
			c.Example = "  " + brand.Name + " help new\n" +
				"  " + brand.Name + " help host add\n" +
				"  " + brand.Name + " help concepts"
		case "completion":
			c.Example = "  " + brand.Name + " completion zsh"
			for _, sh := range c.Commands() {
				sh.Example = completionExample(sh.Name())
			}
		}
	}
	var fit func(c *cobra.Command)
	fit = func(c *cobra.Command) {
		if !IsTopic(c) {
			c.Long = fitLong(c.Long)
		}
		for _, sub := range c.Commands() {
			fit(sub)
		}
	}
	fit(root)
	// Every other command keeps Cobra's own usage, with its flags wrapped.
	addTrimLines.Do(func() { cobra.AddTemplateFunc("trimLines", trimLines) })
	wrapped := fmt.Sprintf("FlagUsagesWrapped %d | trimLines}}", helpWidth)
	root.SetUsageTemplate(strings.NewReplacer(
		"FlagUsages | trimTrailingWhitespaces}}", wrapped,
	).Replace((&cobra.Command{}).UsageTemplate()))
	cobraUsage := (&cobra.Command{}).UsageFunc()
	root.SetUsageFunc(func(c *cobra.Command) error {
		if c.HasParent() {
			return cobraUsage(c)
		}
		return rootUsage(c.OutOrStderr(), c)
	})
	cobraHelp := (&cobra.Command{}).HelpFunc()
	root.SetHelpFunc(func(c *cobra.Command, args []string) {
		if c.HasParent() {
			cobraHelp(c, args)
			return
		}
		// The root's help starts with its short line, as its Long says
		// what its usage line says.
		fmt.Fprintf(c.OutOrStdout(), "%s\n\n%s", c.Short, c.UsageString())
	})
}

// addTrimLines gives Cobra's templates trimLines, once.
var addTrimLines sync.Once

// trimLines removes the spaces at the end of each line of s, and the blank
// lines at its end. The flags' usage, wrapped, leaves spaces after a long
// flag.
func trimLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRightFunc(l, unicode.IsSpace)
	}
	return strings.TrimRightFunc(strings.Join(lines, "\n"), unicode.IsSpace)
}

// fitLong wraps each paragraph of a command's Long at helpWidth, so that
// the source may break its lines anywhere. A line that starts with a space
// or a tab, such as a command to type, stays as it is, and so does a blank
// line. Only its tabs at the start become two spaces each, as a terminal
// would take a tab to column 8.
func fitLong(s string) string {
	var out, para []string
	flush := func() {
		if len(para) > 0 {
			out = append(out, wrap(strings.Join(para, " "), helpWidth)...)
			para = nil
		}
	}
	for _, l := range strings.Split(s, "\n") {
		switch {
		case strings.TrimSpace(l) == "":
			flush()
			out = append(out, "")
		case l[0] == ' ' || l[0] == '\t':
			flush()
			text := strings.TrimLeft(l, "\t")
			out = append(out, strings.Repeat("  ", len(l)-len(text))+text)
		default:
			para = append(para, l)
		}
	}
	flush()
	return strings.Join(out, "\n")
}

// completionExample loads the completion of one shell in the current
// session, as Cobra's own help of each shell says.
func completionExample(shell string) string {
	switch shell {
	case "fish":
		return "  " + brand.Name + " completion fish | source"
	case "powershell":
		return "  " + brand.Name + " completion powershell | Out-String | Invoke-Expression"
	default:
		return "  source <(" + brand.Name + " completion " + shell + ")"
	}
}

// rootUsage writes the usage part of the root's help: what it does alone,
// its examples, its commands in their groups and in the groups' order, its
// flags and its help topics (docs/contributing/cli-spec.md 8.1).
func rootUsage(w io.Writer, c *cobra.Command) error {
	var b strings.Builder
	path := c.CommandPath()
	fmt.Fprintf(&b, "Usage:\n  %-*s  %s\n  %s [command]\n", len(path+" [command]"), path, opensTheLauncher, path)
	if c.HasExample() {
		fmt.Fprintf(&b, "\nExamples:\n%s\n", c.Example)
	}
	for _, g := range helpGroups {
		var lines strings.Builder
		for _, name := range g.Commands {
			if sub := subcommand(c, name); sub != nil && (sub.IsAvailableCommand() || sub.Name() == "help") {
				fmt.Fprintf(&lines, "  %-*s %s\n", sub.NamePadding(), sub.Name(), sub.Short)
			}
		}
		if lines.Len() > 0 {
			fmt.Fprintf(&b, "\n%s:\n%s", g.Title, lines.String())
		}
	}
	if c.HasAvailableLocalFlags() {
		fmt.Fprintf(&b, "\nFlags:\n%s\n", trimLines(c.LocalFlags().FlagUsagesWrapped(helpWidth)))
	}
	var topics strings.Builder
	for _, sub := range c.Commands() {
		if IsTopic(sub) {
			fmt.Fprintf(&topics, "  %-*s %s\n", sub.NamePadding(), sub.Name(), sub.Short)
		}
	}
	if topics.Len() > 0 {
		b.WriteString("\nAdditional help topics:\n" + topics.String())
	}
	fmt.Fprintf(&b, "\nUse \"%s [command] --help\" for more information about a command.\n", path)
	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("write the help: %w", err)
	}
	return nil
}
