package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
)

// The copyable guide and the spec keep scripts, exit codes and help as
// separate bullets, in that order. No future command belongs in the guide.
func TestAgentSnippetKeepsTheRequiredLayout(t *testing.T) {
	repo := filepath.Join("..", "..")
	read := func(path string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(repo, path))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	guide := read("docs/how-to/use-with-agents.md")
	_, fenced, found := strings.Cut(guide, "```markdown\n")
	snippet, _, ended := strings.Cut(fenced, "\n```")
	if !found || !ended || strings.HasPrefix(snippet, "---") || len(strings.Split(snippet, "\n")) > 15 {
		t.Fatalf("the guide has no plain Markdown snippet of at most15 lines:\n%s", guide)
	}
	want := "- In scripts, pass `--no-input`.\n" +
		"- Exit codes: `" + brand.Name + " help exit-codes`\n" +
		"- Help for a command: `" + brand.Name + " <command> --help`"
	if !strings.Contains(snippet, want) {
		t.Fatalf("the guide merged or reordered the required bullets:\n%s", snippet)
	}
	spec := read("docs/contributing/cli-spec.md")
	if !strings.Contains(spec, strings.ReplaceAll(want, "\n", "\n   ")) {
		t.Fatal("the spec and guide disagree on the required bullet layout")
	}
	for _, match := range regexp.MustCompile("`([^`]+)`").FindAllStringSubmatch(snippet, -1) {
		command := match[1]
		if !strings.HasPrefix(command, brand.Name+" ") || strings.Contains(command, "<command>") {
			continue
		}
		c, args, err := root().Find(strings.Fields(command)[1:])
		if err != nil {
			t.Fatalf("the snippet names an unavailable command %q: %v", command, err)
		}
		if err := c.ParseFlags(args); err != nil {
			t.Fatalf("the snippet has unavailable flags in %q: %v", command, err)
		}
		if err := c.ValidateArgs(c.Flags().Args()); err != nil {
			t.Fatalf("the snippet has invalid arguments in %q: %v", command, err)
		}
	}
}
