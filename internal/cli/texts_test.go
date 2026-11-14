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

// The CHANGELOG and cli-spec 5.1 rule 7 name the same two exceptions to "no
// host is saved after a cancel" as the code has: the only saves of the
// hosts that a cancel does not stop are host destroy's, once the machine is
// gone (client.ForgetHost), and host create's, once the machine is made
// (onboard.Create).
func TestTheCancelsExceptionsAreTheCodes(t *testing.T) {
	changelog, err := os.ReadFile(filepath.Join("..", "..", "CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(changelog), "After a cancel, the client sends a host no more requests and saves no host, with two exceptions: `host create` saves a machine that it has made, and `host destroy` forgets a host whose machine it has deleted.") {
		t.Error("the CHANGELOG does not name the two exceptions")
	}
	spec, err := os.ReadFile(filepath.Join("..", "..", "docs", "contributing", "cli-spec.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(spec), "`host destroy` forgets a host whose machine it has deleted, and `host create` saves a machine that it has made.") {
		t.Error("cli-spec 5.1 rule 7 does not name the two exceptions")
	}
	var found []string
	err = filepath.WalkDir(filepath.Join("..", ".."), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(src), "\n") {
			if strings.Contains(line, "SaveHost") && (strings.Contains(line, "WithoutCancel") || strings.Contains(line, "context.Background()")) {
				rel, _ := filepath.Rel(filepath.Join("..", ".."), p)
				found = append(found, filepath.ToSlash(rel))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"internal/client/machine.go", "internal/onboard/create.go"}; strings.Join(found, " ") != strings.Join(want, " ") {
		t.Errorf("the saves that a cancel does not stop are in %q, want %q", found, want)
	}
}
