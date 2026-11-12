package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// stdinAllowed lists the functions, outside answer.go, that hand os.Stdin
// to code that reads it only through answer. Each has its reason.
var stdinAllowed = map[string]string{
	"newGuide":     "the guide reads g.in only through readLine, which reads through answer",
	"startSession": "askSetup and askMakeFolder read through question, which reads through answer",
	"doctorCmd":    "offerFixes reads its answers through question",
	"fixHost":      "offerFixes reads its answers through question",
}

// lineReads are the calls that read a line or a buffer of their own. Only
// answer.go may make them.
var lineReads = map[string]bool{
	"bufio.NewReader": true, "bufio.NewScanner": true, "bufio.NewReaderSize": true,
	"fmt.Scan": true, "fmt.Scanf": true, "fmt.Scanln": true, "fmt.Fscan": true, "fmt.Fscanf": true, "fmt.Fscanln": true,
	"io.ReadAll": true, "io.ReadFull": true,
}

// Every read of what a person types goes through answer
// (docs/contributing/cli-spec.md 5.1, rule 7), so that every question ends
// at a cancel. Outside answer.go, os.Stdin may only be asked whether it is
// a terminal (isTerminal, Fd), be a child process's stdin, be opened
// through inputOf, or be handed on by a function of stdinAllowed. No file
// but answer.go makes a reader of lines or calls ReadString, ReadLine,
// ReadRune or ReadByte. The check reads the source: it cannot follow a
// reader that a function of stdinAllowed passes on, which is why each
// entry gives its reason.
func TestNoDirectReadOfStdin(t *testing.T) {
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	seen := 0
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") || p == "answer.go" {
			continue
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, problem := range stdinReads(fset, f) {
			t.Error(problem)
		}
		seen++
	}
	if seen < 20 {
		t.Fatalf("read only %d files", seen)
	}
}

// TestTheStdinCheckFindsADirectRead runs the check on a file that reads
// os.Stdin in each way that it forbids.
func TestTheStdinCheckFindsADirectRead(t *testing.T) {
	src := `package cli

func a() { os.Stdin.Read(nil) }
func b() { _ = bufio.NewReader(os.Stdin) }
func c(in io.Reader) { _, _ = bufio.NewReader(in).ReadString('\n') }
func d() { var s string; fmt.Scanln(&s) }
func e() { use(os.Stdin) }
func f() { _ = isTerminal(os.Stdin); _ = os.Stdin.Fd(); cmd.Stdin = os.Stdin; inputOf(os.Stdin) }
func newGuide() { use(os.Stdin) }
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(stdinReads(fset, f), "\n")
	for _, line := range []string{"x.go:3:", "x.go:4:", "x.go:5:", "x.go:6:", "x.go:7:"} {
		if !strings.Contains(got, line) {
			t.Errorf("no problem at %s in:\n%s", line, got)
		}
	}
	for _, line := range []string{"x.go:8:", "x.go:9:"} {
		if strings.Contains(got, line) {
			t.Errorf("a problem at %s, which is allowed:\n%s", line, got)
		}
	}
}

// stdinReads returns a problem for each read of what a person types in f
// that does not go through answer.
func stdinReads(fset *token.FileSet, f *ast.File) []string {
	var problems []string
	bad := func(n ast.Node, what string) {
		problems = append(problems, fset.Position(n.Pos()).String()+": "+what)
	}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		allowed := stdinAllowed[fn.Name.Name] != ""
		var stack []ast.Node
		ast.Inspect(fn, func(n ast.Node) bool {
			if n == nil {
				stack = stack[:len(stack)-1]
				return true
			}
			if call, ok := n.(*ast.CallExpr); ok {
				if name := callName(call); lineReads[name] {
					bad(call, "a reader of its own: "+name+"; read through answer")
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					switch sel.Sel.Name {
					case "ReadString", "ReadLine", "ReadRune", "ReadByte":
						bad(call, sel.Sel.Name+": read through answer")
					}
				}
			}
			if isStdin(n) && !allowed && !stdinUseIsSafe(stack) {
				bad(n, "os.Stdin read outside answer.go; read through answer, or add the function to stdinAllowed with a reason")
			}
			stack = append(stack, n)
			return true
		})
	}
	return problems
}

// isStdin reports whether n is os.Stdin.
func isStdin(n ast.Node) bool {
	sel, ok := n.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Stdin" {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && x.Name == "os"
}

// stdinUseIsSafe reports whether os.Stdin, under the nodes of stack, reads
// nothing: it is asked whether it is a terminal, is a child process's
// stdin, or is opened through inputOf.
func stdinUseIsSafe(stack []ast.Node) bool {
	if len(stack) == 0 {
		return false
	}
	switch p := stack[len(stack)-1].(type) {
	case *ast.SelectorExpr:
		return p.Sel.Name == "Fd"
	case *ast.CallExpr:
		name := callName(p)
		return name == "isTerminal" || name == "inputOf"
	case *ast.AssignStmt:
		for i, r := range p.Rhs {
			if isStdin(r) && i < len(p.Lhs) {
				if sel, ok := p.Lhs[i].(*ast.SelectorExpr); ok && sel.Sel.Name == "Stdin" {
					return true
				}
			}
		}
	}
	return false
}

// callName is the name of the function that call calls: "f", or "pkg.f".
func callName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		if x, ok := fn.X.(*ast.Ident); ok {
			return x.Name + "." + fn.Sel.Name
		}
	}
	return ""
}
