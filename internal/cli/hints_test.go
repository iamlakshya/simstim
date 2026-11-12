package cli

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

// clientPath is the import path of the client package.
const clientPath = "github.com/iamlakshya/simstim/internal/client"

// Every client.UserError that the client builds names its error with a
// Code and has a Try: line (docs/contributing/cli-spec.md 4.6). Test files
// are left out: tests build bare errors on purpose.
//
// The check reads the source, so it finds what the source shows: a
// UserError literal, under any name of the client package; a Code or a
// Hint that is missing, or that is an empty string made of constants, such
// as "" + ""; a literal without field names; a UserError made with new()
// or declared as a zero value, which starts without either; and an empty
// constant set into a field named Code or Hint later. It cannot tell a
// value that is empty only when the client runs, such as Hint: hint with
// an empty hint, nor whose field an assignment sets. describe gives such an
// error a Try: line anyway (TestDescribeAlwaysHasATry).
func TestEveryUserErrorHasHint(t *testing.T) {
	fset := token.NewFileSet()
	count := 0
	err := filepath.WalkDir("..", func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return err
		}
		n, problems := checkUserErrors(fset, f)
		count += n
		for _, pr := range problems {
			t.Error(pr)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count < 100 {
		t.Errorf("found only %d UserError literals: the test no longer reads the code", count)
	}
}

// The check finds each way to build a UserError without a Code or a hint.
func TestTheHintCheckFindsEachMistake(t *testing.T) {
	src := `package x

import cl "` + clientPath + `"

func a() error { return &cl.UserError{Msg: "m", Hint: "h"} }
func b() error { return &cl.UserError{Msg: "m", Code: "c", Hint: "" + ""} }
func c() error { return new(cl.UserError) }
func d() error { var e cl.UserError; return &e }
func e() error { return &cl.UserError{"m", "h", "c"} }
func f(u *cl.UserError) { u.Hint = "" }
func g() error { return &cl.UserError{Msg: "m", Code: "c", Hint: "h"} }
func h() error { return &cl.UserError{Msg: "m", Code: "c", Hint: "ssh box" + "   (a note)"} }
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	n, problems := checkUserErrors(fset, f)
	if n != 5 {
		t.Errorf("found %d literals, want 5", n)
	}
	for _, want := range []string{"x.go:5:", "x.go:6:", "x.go:7:", "x.go:8:", "x.go:9:", "x.go:10:", "x.go:12:"} {
		found := false
		for _, p := range problems {
			found = found || strings.HasPrefix(p, want)
		}
		if !found {
			t.Errorf("no problem at %s in %q", want, problems)
		}
	}
	for _, p := range problems {
		if strings.HasPrefix(p, "x.go:11:") {
			t.Errorf("a good literal: %s", p)
		}
	}
}

// userErrorType returns a check of whether an expression of f names the
// type client.UserError, under the name that f gives the client package.
func userErrorType(f *ast.File) func(ast.Expr) bool {
	local, dot := "", f.Name.Name == "client"
	for _, imp := range f.Imports {
		if path, _ := strconv.Unquote(imp.Path.Value); path == clientPath {
			local = "client"
			if imp.Name != nil {
				local = imp.Name.Name
			}
			dot = dot || local == "."
		}
	}
	return func(e ast.Expr) bool {
		switch e := e.(type) {
		case *ast.SelectorExpr:
			x, ok := e.X.(*ast.Ident)
			return ok && local != "" && x.Name == local && e.Sel.Name == "UserError"
		case *ast.Ident:
			return dot && e.Name == "UserError"
		}
		return false
	}
}

// checkUserErrors returns the number of UserError literals in f, and a
// problem for each one without a Code or a hint.
func checkUserErrors(fset *token.FileSet, f *ast.File) (int, []string) {
	isUserError := userErrorType(f)
	count := 0
	var problems []string
	bad := func(n ast.Node, what string) {
		problems = append(problems, fset.Position(n.Pos()).String()+": "+what)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CompositeLit:
			if !isUserError(n.Type) {
				return true
			}
			count++
			fields := map[string]ast.Expr{}
			for _, el := range n.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					bad(n, "a UserError literal without field names")
					return true
				}
				if k, ok := kv.Key.(*ast.Ident); ok {
					fields[k.Name] = kv.Value
				}
			}
			for _, name := range []string{"Code", "Hint"} {
				if v, ok := fields[name]; !ok || emptyConst(v) {
					bad(n, "a UserError without a "+name)
				}
			}
			if h, ok := fields["Hint"]; ok {
				ast.Inspect(h, func(n ast.Node) bool {
					if lit, ok := n.(*ast.BasicLit); ok && strings.Contains(lit.Value, "  (") {
						bad(lit, "a hint's note in brackets after more than one space (style.md: one)")
					}
					return true
				})
			}
		case *ast.CallExpr:
			if id, ok := n.Fun.(*ast.Ident); ok && id.Name == "new" && len(n.Args) == 1 && isUserError(n.Args[0]) {
				bad(n, "new(UserError) makes one without a Code or a hint")
			}
		case *ast.ValueSpec:
			if n.Type != nil && isUserError(n.Type) && len(n.Values) == 0 {
				bad(n, "a zero UserError has no Code or hint")
			}
		case *ast.AssignStmt:
			for i, l := range n.Lhs {
				if sel, ok := l.(*ast.SelectorExpr); ok && (sel.Sel.Name == "Code" || sel.Sel.Name == "Hint") && i < len(n.Rhs) && emptyConst(n.Rhs[i]) {
					bad(n, "an empty "+sel.Sel.Name)
				}
			}
		}
		return true
	})
	return count, problems
}

// emptyConst reports whether e is a string constant, made of literals,
// that is empty.
func emptyConst(e ast.Expr) bool {
	s, ok := constString(e)
	return ok && s == ""
}

func constString(e ast.Expr) (string, bool) {
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(e.Value)
		return s, err == nil
	case *ast.ParenExpr:
		return constString(e.X)
	case *ast.BinaryExpr:
		a, ok1 := constString(e.X)
		b, ok2 := constString(e.Y)
		return a + b, ok1 && ok2 && e.Op == token.ADD
	}
	return "", false
}

// literalKinds maps each Code of a UserError literal under internal/, but
// tests, to the name of its Kind: "KindFailed" when it has none.
func literalKinds(t *testing.T) map[string]string {
	t.Helper()
	kinds := map[string]string{}
	fset := token.NewFileSet()
	err := filepath.WalkDir("..", func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return err
		}
		isUserError := userErrorType(f)
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok || !isUserError(lit.Type) {
				return true
			}
			code, kind := "", "KindFailed"
			for _, el := range lit.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				k, _ := kv.Key.(*ast.Ident)
				switch {
				case k == nil:
				case k.Name == "Code":
					code, _ = constString(kv.Value)
				case k.Name == "Kind":
					if sel, ok := kv.Value.(*ast.SelectorExpr); ok {
						kind = sel.Sel.Name
					} else if id, ok := kv.Value.(*ast.Ident); ok {
						kind = id.Name
					}
				}
			}
			if code != "" {
				kinds[code] = kind
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return kinds
}

// One code has one kind, also in the errors that the CLI makes as it runs:
// each has the kind of the literals with its code (TestOneCodeOneKind
// checks the literals against each other and the host's codes).
func TestRunTimeErrorsKeepOneKind(t *testing.T) {
	lits := literalKinds(t)
	names := map[client.Kind]string{client.KindFailed: "KindFailed", client.KindUsage: "KindUsage", client.KindNotFound: "KindNotFound",
		client.KindNeedsAnswer: "KindNeedsAnswer", client.KindRefused: "KindRefused", client.KindCancelled: "KindCancelled"}
	hosttest.New(t)
	var errs []error
	for _, args := range [][]string{{"atach"}, {"host", "ad"}, {"help", "atach"}, {"new", "--detatch"}, {"new", "--dir"},
		{"debug", "start", "a/b", "--for", "soon"}, {"attach"}, {"ls", "box"}, {"---x"}, {"attach", "nowhere/s"}} {
		_, err := line(t, args...)
		errs = append(errs, err)
	}
	errs = append(errs, errInterrupted,
		answeredNo(&client.UserError{Msg: "no folder", Hint: "Pass --mkdir.", Code: api.CodeNoFolder, Kind: client.KindNeedsAnswer}))
	path := filepath.Join(client.ConfigDir(), "hosts.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[[host]]\nname = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := client.LoadHosts()
	errs = append(errs, err)
	for _, err := range errs {
		var ue *client.UserError
		if !errors.As(err, &ue) {
			t.Errorf("%v is not a user error", err)
			continue
		}
		if want, ok := lits[ue.Code]; !ok || names[ue.Kind] != want {
			t.Errorf("%q has the code %s and %s, and the literals give that code %q", ue.Msg, ue.Code, names[ue.Kind], want)
		}
	}
}
