package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/cli"
)

// A command's page shows its kept aliases, as a user types them
// (docs/contributing/cli-spec.md 8.5). TestPagesListOldNames checks its old
// names, which come from the table of internal/cli/deprecated.go.
func TestPageShowsAliases(t *testing.T) {
	run := func(*cobra.Command, []string) {}
	r := &cobra.Command{Use: brand.Name}
	rm := &cobra.Command{Use: "rm <host>/<session>", Aliases: []string{"delete"}, Short: "Delete a session", Example: "  x", Run: run}
	host := &cobra.Command{Use: "host", Short: "Hosts"}
	hostRm := &cobra.Command{Use: "rm <host>", Aliases: []string{"remove", "forget"}, Short: "Forget a host", Example: "  x", Run: run}
	ls := &cobra.Command{Use: "ls", Short: "List", Example: "  x", Run: run}
	host.AddCommand(hostRm)
	r.AddCommand(rm, host, ls)

	for _, c := range []struct {
		cmd  *cobra.Command
		want string
	}{
		{rm, "\n**Aliases:** `delete`\n"},
		{hostRm, "\n**Aliases:** `host remove`, `host forget`\n"},
	} {
		if page := commandPage(c.cmd); !strings.Contains(page, c.want) {
			t.Errorf("the page of %s has no %q:\n%s", c.cmd.CommandPath(), c.want, page)
		}
	}
	if page := commandPage(ls); strings.Contains(page, "Aliases") {
		t.Errorf("a command without aliases shows them:\n%s", page)
	}
}

// Every help topic of the client gets a page of its own, with the prose of
// the topic.
func TestEveryTopicHasAPage(t *testing.T) {
	pages := map[string][]byte{}
	if err := topicPages(pages); err != nil {
		t.Fatal(err)
	}
	var topics int
	for _, c := range root().Commands() {
		if cli.IsTopic(c) {
			topics++
			if !strings.Contains(string(pages[c.Name()+".md"]), "help "+c.Name()+"` prints the same") {
				t.Errorf("the page of the topic %s does not say which command prints it:\n%s", c.Name(), pages[c.Name()+".md"])
			}
		}
	}
	if topics < 2 || len(pages) != topics {
		t.Errorf("%d topics, %d pages", topics, len(pages))
	}
	for page, texts := range map[string][]string{
		"concepts.md":   {cli.ConceptsIntro},
		"exit-codes.md": {cli.ExitCodesIntro, cli.ExitCodesOutro},
	} {
		for _, p := range texts {
			if !strings.Contains(string(pages[page]), prose(p)) {
				t.Errorf("%s does not hold the topic's prose %q:\n%s", page, p, pages[page])
			}
		}
	}
}

// The command index lists every command of each group of the root help,
// help and completion too, under that group, and each one has a page.
func TestIndexListsEveryGroupedCommand(t *testing.T) {
	pages := map[string][]byte{}
	if err := cliPages(pages); err != nil {
		t.Fatal(err)
	}
	index := string(pages[filepath.Join("cli", "_index.md")])
	for _, g := range cli.HelpGroups() {
		start := strings.Index(index, "\n## "+g.Title+"\n")
		if start < 0 {
			t.Errorf("the index has no group %q:\n%s", g.Title, index)
			continue
		}
		section := index[start+1:]
		if end := strings.Index(section, "\n## "); end >= 0 {
			section = section[:end]
		}
		for _, name := range g.Commands {
			if !strings.Contains(section, "| [`"+name+"`]("+name+".md) |") {
				t.Errorf("the group %q of the index does not list %s:\n%s", g.Title, name, section)
			}
			if _, ok := pages[filepath.Join("cli", name+".md")]; !ok {
				t.Errorf("%s has no page", name)
			}
		}
	}
	for _, sh := range []string{"bash", "fish", "powershell", "zsh"} {
		if _, ok := pages[filepath.Join("cli", "completion_"+sh+".md")]; !ok {
			t.Errorf("completion %s has no page", sh)
		}
	}
}

func TestProseFormatsCodeAndHeadings(t *testing.T) {
	in := "Intro line one\nline two.\n\n#### Linux:\n\n  " + brand.Name + " completion zsh > /tmp/x\n\nDone."
	want := "Intro line one line two.\n\n## Linux\n\n```sh\n" + brand.Name + " completion zsh > /tmp/x\n```\n\nDone."
	if got := prose(in); got != want {
		t.Errorf("prose =\n%s\nwant:\n%s", got, want)
	}
}
