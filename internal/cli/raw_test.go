package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// rawFiles are the files that put the terminal in raw mode for a question
// or the picker. They do it only through keyread (Reader.Raw), whose
// restore goes back to the state that keyread saved before it made its
// reader. A direct MakeRaw and Restore would put back what the reader
// changed, on Windows the console's modes (cli-spec 6.2, rule 1).
var rawFiles = []string{"answer.go", filepath.Join("..", "tui", "picker_run.go")}

// termPackages are the packages whose MakeRaw and Restore only keyread
// may call.
var termPackages = map[string]bool{"golang.org/x/term": true, "github.com/charmbracelet/x/term": true}

func TestRawModeGoesThroughKeyread(t *testing.T) {
	fset := token.NewFileSet()
	for _, p := range rawFiles {
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, problem := range rawCalls(fset, f) {
			t.Error(problem)
		}
	}
}

// TestTheRawCheckFindsADirectCall runs the check on a file that calls
// MakeRaw and Restore of both terminal packages, under any name.
func TestTheRawCheckFindsADirectCall(t *testing.T) {
	src := `package x

import (
	"golang.org/x/term"
	xt "github.com/charmbracelet/x/term"
)

func a() { term.MakeRaw(0) }
func b() { xt.Restore(0, nil) }
func c() { _ = term.IsTerminal(0) }
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(rawCalls(fset, f), "\n")
	for _, line := range []string{"x.go:8:", "x.go:9:"} {
		if !strings.Contains(got, line) {
			t.Errorf("no problem at %s in:\n%s", line, got)
		}
	}
	if strings.Contains(got, "x.go:10:") {
		t.Errorf("a problem at x.go:10, which is allowed:\n%s", got)
	}
}

// rawCalls returns a problem for each call in f to MakeRaw or Restore of
// a terminal package.
func rawCalls(fset *token.FileSet, f *ast.File) []string {
	names := map[string]bool{}
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || !termPackages[path] {
			continue
		}
		name := "term"
		if imp.Name != nil {
			name = imp.Name.Name
		}
		names[name] = true
	}
	var problems []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if x, ok := sel.X.(*ast.Ident); ok && names[x.Name] && (sel.Sel.Name == "MakeRaw" || sel.Sel.Name == "Restore") {
			problems = append(problems, fset.Position(call.Pos()).String()+": "+x.Name+"."+sel.Sel.Name+
				": put the terminal in raw mode and back through keyread's Reader.Raw")
		}
		return true
	})
	return problems
}
