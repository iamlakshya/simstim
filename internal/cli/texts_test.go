package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// docOf is the doc comment of the function name in the Go file file of
// this package.
func docOf(t *testing.T, file, name string) string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == name && fn.Recv == nil {
			return fn.Doc.Text()
		}
	}
	t.Fatalf("no func %s in %s", name, file)
	return ""
}

// The texts that say how a question reads say what the paste blocks
// change: rule 1 of cli-spec 6.2, and answerOn's comment. applyAll and
// applyLocal each have their own comment.
func TestTheTextsOfTheQuestionsAndTheFixes(t *testing.T) {
	spec, err := os.ReadFile(filepath.Join("..", "..", "docs", "contributing", "cli-spec.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, rules, _ := strings.Cut(string(spec), "### 6.2 Prompts\n")
	rule1, _, _ := strings.Cut(rules, "\n2. ")
	if !strings.Contains(rule1, "It reads a paste in blocks, so the last block of a paste can hold keys typed within moments of it") {
		t.Error("cli-spec 6.2 rule 1 does not say what the paste blocks change")
	}
	if doc := docOf(t, "answer.go", "answerOn"); !strings.Contains(doc, "Only a paste is read in blocks") {
		t.Errorf("answerOn's comment does not say what the paste blocks change:\n%s", doc)
	}
	for _, name := range []string{"applyAll", "applyLocal"} {
		if doc := docOf(t, "fix.go", name); !strings.HasPrefix(doc, name+" applies") || strings.Count(doc, " applies ") > 2 {
			t.Errorf("%s's comment is not its own:\n%s", name, doc)
		}
	}
}

// The general cancellation rule and the host command's rule agree: saves
// stop with their caller. Machine cleanup and completed-step reporting do not
// grant a cancelled command permission to save or forget a host.
func TestTheCancellationPolicyMatchesHostSaves(t *testing.T) {
	changelog, err := os.ReadFile(filepath.Join("..", "..", "CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(changelog), "A machine whose initial host save already committed stays saved if later preparation stops, with no further host write.") {
		t.Error("the CHANGELOG does not distinguish committed creation from later preparation")
	}
	if !strings.Contains(string(changelog), "After a cancel, the client sends a host no more requests and saves no host.") {
		t.Error("the CHANGELOG does not state the host-save cancellation policy")
	}
	spec, err := os.ReadFile(filepath.Join("..", "..", "docs", "contributing", "cli-spec.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, general, _ := strings.Cut(string(spec), "### 5.1 The table")
	general, _, _ = strings.Cut(general, "### 5.2 `exec`")
	_, hosts, _ := strings.Cut(string(spec), "### 11.3 `host rm`")
	hosts, _, _ = strings.Cut(hosts, "### 11.4 `host upgrade`")
	if !strings.Contains(general, "A cancelled command never writes `hosts.toml`.") ||
		!strings.Contains(hosts, "A cancelled command never saves:") ||
		!strings.Contains(general, "bounded cleanup under the held host lock deletes") ||
		!strings.Contains(general, "Once that initial save commits") ||
		!strings.Contains(general, "a cancelled forget keeps the saved host") {
		t.Error("the general and host cancellation rules do not agree")
	}
}
