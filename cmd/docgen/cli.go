package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"
	"github.com/spf13/pflag"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/cli"
)

// root builds the client's command tree for the docs.
func root() *cobra.Command {
	r := cli.Root()
	r.DisableAutoGenTag = true
	return r
}

// pageName is a command's page: "host add" becomes host_add.md, and the root
// command is index.md. File names leave out the product's name, so a rename
// changes no paths.
func pageName(path string) string {
	rest := strings.TrimSpace(strings.TrimPrefix(path, brand.Name))
	if rest == "" {
		return "_index.md"
	}
	return strings.ReplaceAll(rest, " ", "_") + ".md"
}

// cliPages adds one page per visible command under cli/, and the commands'
// index, cli/_index.md.
func cliPages(pages map[string][]byte) error {
	r := root()
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.HasParent() {
			pages[filepath.Join("cli", pageName(c.CommandPath()))] = []byte(header(c.CommandPath(), "") + commandPage(c))
		}
		for _, sub := range visible(c) {
			walk(sub)
		}
	}
	walk(r)
	pages[filepath.Join("cli", pageName(r.CommandPath()))] = []byte(header("Commands", "1") + commandsIndex(r))
	return nil
}

// visible lists a command's subcommands that a user can run.
func visible(c *cobra.Command) []*cobra.Command {
	var out []*cobra.Command
	for _, sub := range c.Commands() {
		if sub.IsAvailableCommand() && !sub.IsAdditionalHelpTopicCommand() {
			out = append(out, sub)
		}
	}
	return out
}

// commandPage is one command's page: what it does, its usage, the long
// help, examples, flags, subcommands and where it sits in the tree.
func commandPage(c *cobra.Command) string {
	var b strings.Builder
	b.WriteString(prose(sentence(c.Short)) + "\n\n")
	fmt.Fprintf(&b, "```text\n%s\n```\n\n", c.UseLine())
	if c.Long != "" {
		b.WriteString(prose(c.Long) + "\n\n")
	}
	if ex := dedent(c.Example); ex != "" {
		fmt.Fprintf(&b, "## Examples\n\n```sh\n%s\n```\n\n", ex)
	}
	if t := flagTable(c.NonInheritedFlags()); t != "" {
		b.WriteString("## Flags\n\n" + t + "\n")
	}
	if t := flagTable(c.InheritedFlags()); t != "" {
		b.WriteString("## Flags from the parent command\n\n" + t + "\n")
	}
	if subs := visible(c); len(subs) > 0 {
		b.WriteString("## Commands\n\n| Command | What it does |\n| --- | --- |\n")
		for _, sub := range subs {
			fmt.Fprintf(&b, "| [`%s`](%s) | %s |\n", shortPath(sub), pageName(sub.CommandPath()), cell(sub.Short))
		}
		b.WriteString("\n")
	}
	p := c.Parent()
	if p.HasParent() {
		fmt.Fprintf(&b, "Part of [`%s`](%s): %s.\n", shortPath(p), pageName(p.CommandPath()), cell(lowerFirst(p.Short)))
	} else {
		b.WriteString("See [all commands](_index.md).\n")
	}
	return b.String()
}

// commandsIndex is the commands' index: the bare command, then every
// command and subcommand in one table, in the order of the tree.
func commandsIndex(r *cobra.Command) string {
	var b strings.Builder
	b.WriteString(prose(sentence(r.Short)) + "\n\n")
	fmt.Fprintf(&b, "```text\n%s\n```\n\n", r.UseLine())
	if r.Long != "" {
		b.WriteString(prose(r.Long) + "\n\n")
	}
	b.WriteString("## Every command\n\n| Command | What it does |\n| --- | --- |\n")
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range visible(c) {
			fmt.Fprintf(&b, "| [`%s`](%s) | %s |\n", shortPath(sub), pageName(sub.CommandPath()), cell(sub.Short))
			walk(sub)
		}
	}
	walk(r)
	if t := flagTable(r.NonInheritedFlags()); t != "" {
		b.WriteString("\n## Flags\n\n" + t)
	}
	return b.String()
}

// flagTable lists a command's flags, without the help flag every command has.
func flagTable(fs *pflag.FlagSet) string {
	var rows []string
	fs.VisitAll(func(f *pflag.Flag) {
		if f.Hidden || f.Deprecated != "" || f.Name == "help" {
			return
		}
		name, usage := pflag.UnquoteUsage(f)
		flag := "`--" + f.Name
		switch {
		case name == "" || name == "bool":
		case f.NoOptDefVal != "":
			// The value is optional, and binds only in the = form.
			flag += "[=<" + name + ">]"
		default:
			flag += " <" + name + ">"
		}
		flag += "`"
		if f.Shorthand != "" {
			flag = "`-" + f.Shorthand + "`, " + flag
		}
		what := cell(usage)
		if !zeroDefault(f) && !strings.Contains(usage, "(default") {
			what += " (default: `" + f.DefValue + "`)"
		}
		rows = append(rows, fmt.Sprintf("| %s | %s |", flag, what))
	})
	if len(rows) == 0 {
		return ""
	}
	return "| Flag | What it does |\n| --- | --- |\n" + strings.Join(rows, "\n") + "\n"
}

// zeroDefault reports whether a flag's default is its type's zero value,
// which the table does not mention.
func zeroDefault(f *pflag.Flag) bool {
	switch f.DefValue {
	case "", "false", "0", "0s", "[]", "<nil>":
		return true
	}
	return false
}

// shortPath is a command's path without the product's name: "host add".
func shortPath(c *cobra.Command) string {
	return strings.TrimSpace(strings.TrimPrefix(c.CommandPath(), brand.Name))
}

// prose turns help text into Markdown paragraphs: lines of one paragraph
// join, flags and commands in it become code, and characters Markdown would
// read as markup are escaped.
func prose(s string) string {
	blocks := strings.Split(strings.TrimSpace(s), "\n\n")
	paras := make([]string, 0, len(blocks))
	for _, p := range blocks {
		lines := strings.Fields(strings.ReplaceAll(p, "\n", " "))
		paras = append(paras, inline(strings.Join(lines, " ")))
	}
	return strings.Join(paras, "\n\n")
}

// cell is help text inside a table cell.
func cell(s string) string {
	return strings.ReplaceAll(inline(strings.Join(strings.Fields(s), " ")), "|", "\\|")
}

var (
	flagWord = regexp.MustCompile(`(^|[\s(])(--[a-z][a-z0-9-]*(?:=[^\s,;)]*)?)`)
	webAddr  = regexp.MustCompile(`https?://[^\s)]+`)
	escapes  = strings.NewReplacer("\\", "\\\\", "<", "&lt;", ">", "&gt;", "*", "\\*")
)

// inline escapes one line of help text for Markdown, outside code spans,
// and puts flags and web addresses in code spans.
func inline(s string) string {
	text := func(t string) string { return flagWord.ReplaceAllString(escapes.Replace(t), "$1`$2`") }
	parts := strings.Split(s, "`")
	for i := 0; i < len(parts); i += 2 { // the even parts are outside code spans
		var b strings.Builder
		at := 0
		for _, m := range webAddr.FindAllStringIndex(parts[i], -1) {
			addr := strings.TrimRight(parts[i][m[0]:m[1]], ".,;:")
			b.WriteString(text(parts[i][at:m[0]]) + "`" + addr + "`")
			at = m[0] + len(addr)
		}
		b.WriteString(text(parts[i][at:]))
		parts[i] = b.String()
	}
	return strings.Join(parts, "`")
}

// sentence ends a short description with a full stop.
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s != "" && !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// dedent removes the indent cobra's examples share.
func dedent(s string) string {
	lines := strings.Split(strings.Trim(s, "\n"), "\n")
	indent := -1
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		n := len(l) - len(strings.TrimLeft(l, " \t"))
		if indent < 0 || n < indent {
			indent = n
		}
	}
	for i, l := range lines {
		if len(l) >= indent && indent > 0 {
			lines[i] = l[indent:]
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// manPages writes section 1 man pages, one per command.
func manPages(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("make %s: %w", dir, err)
	}
	h := &doc.GenManHeader{Title: strings.ToUpper(brand.Name), Section: "1", Source: brand.DisplayName, Manual: brand.DisplayName + " manual"}
	if err := doc.GenManTree(root(), h, dir); err != nil {
		return fmt.Errorf("man pages: %w", err)
	}
	return nil
}

// completions writes the bash, zsh, fish and PowerShell completion scripts.
func completions(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("make %s: %w", dir, err)
	}
	r := root()
	for _, c := range []struct {
		file string
		gen  func(*bytes.Buffer) error
	}{
		{brand.Name + ".bash", func(b *bytes.Buffer) error { return r.GenBashCompletionV2(b, true) }},
		{"_" + brand.Name, func(b *bytes.Buffer) error { return r.GenZshCompletion(b) }},
		{brand.Name + ".fish", func(b *bytes.Buffer) error { return r.GenFishCompletion(b, true) }},
		{brand.Name + ".ps1", func(b *bytes.Buffer) error { return r.GenPowerShellCompletionWithDesc(b) }},
	} {
		var b bytes.Buffer
		if err := c.gen(&b); err != nil {
			return fmt.Errorf("completion %s: %w", c.file, err)
		}
		if err := os.WriteFile(filepath.Join(dir, c.file), b.Bytes(), 0o644); err != nil {
			return fmt.Errorf("write completion: %w", err)
		}
	}
	return nil
}
