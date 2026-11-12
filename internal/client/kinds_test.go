package client

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestKindNames(t *testing.T) {
	for k, want := range map[Kind]string{
		KindFailed: "failed", KindUsage: "usage", KindNotFound: "not_found",
		KindNeedsAnswer: "needs_answer", KindRefused: "refused", KindCancelled: "cancelled", Kind(99): "failed",
	} {
		if got := k.String(); got != want {
			t.Errorf("Kind(%d) = %q, want %q", k, got, want)
		}
	}
}

func TestHostErrorKeepsItsCodeAndKind(t *testing.T) {
	for code, want := range map[string]Kind{
		"not_found": KindNotFound, "no_folder": KindNeedsAnswer, "setup_trust": KindNeedsAnswer,
		"bad_request": KindFailed, "wall_floor": KindRefused, "layer_missing": KindNeedsAnswer,
		"ssh_key_kind": KindUsage, "rewind_unknown": KindNotFound, "a_code_of_a_later_helper": KindFailed,
		// The kind of the 5.1 table, the same as the client's own check.
		"unknown_agent": KindNotFound, "memory_max": KindUsage, "bad_name": KindUsage, "bad_secret": KindUsage,
	} {
		e := hostError(&proto.Error{Code: code, Message: "m", Hint: "h", Data: json.RawMessage(`{"a":1}`)})
		if e.Kind != want || e.Code != code || e.Msg != "m" || e.Hint != "h" || string(e.Data) != `{"a":1}` {
			t.Errorf("hostError(%s) = %+v, want kind %v", code, e, want)
		}
	}
}

// A host's message and hint reach the terminal on one line, without the
// bytes that a terminal acts on.
func TestHostErrorIsCleaned(t *testing.T) {
	e := hostError(&proto.Error{Code: "x", Message: "no session \x1bc\x1b]0;title\x07abc\nnext", Hint: "Try\tit \x1b[2Jagain."})
	if e.Msg != "no session c]0;titleabc next" || e.Hint != "Try it [2Jagain." {
		t.Errorf("hostError = %q, %q", e.Msg, e.Hint)
	}
}

// Every error code that today's helper sends, and every code in
// internal/api, has a kind in hostKinds (docs/contributing/cli-spec.md
// 5.1, rule 6). A code that a host passes in a variable cannot be read
// here, so its literal must be in the table too.
func TestHostCodesHaveAKind(t *testing.T) {
	fset := token.NewFileSet()
	consts := map[string]string{}
	for _, f := range parseDir(t, "../api", fset) {
		ast.Inspect(f, func(n ast.Node) bool {
			if v, ok := n.(*ast.ValueSpec); ok {
				for i, name := range v.Names {
					if strings.HasPrefix(name.Name, "Code") && i < len(v.Values) {
						if s, ok := stringLit(v.Values[i]); ok {
							consts[name.Name] = s
						}
					}
				}
			}
			return true
		})
	}
	if len(consts) < 3 {
		t.Fatalf("found %d codes in internal/api", len(consts))
	}
	codes := map[string]string{}
	for name, c := range consts {
		codes[c] = "internal/api " + name
	}
	value := func(e ast.Expr) (string, bool) {
		if s, ok := stringLit(e); ok {
			return s, true
		}
		if sel, ok := e.(*ast.SelectorExpr); ok {
			if x, ok := sel.X.(*ast.Ident); ok && x.Name == "api" {
				s, ok := consts[sel.Sel.Name]
				return s, ok
			}
		}
		return "", false
	}
	for _, f := range parseDir(t, "../host", fset) {
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CallExpr:
				// UserError(code, …), questionError(code, …), and a fail(code, …) of secrets.go
				if id, ok := n.Fun.(*ast.Ident); ok && (id.Name == "UserError" || id.Name == "questionError" || id.Name == "fail") && len(n.Args) > 0 {
					if c, ok := value(n.Args[0]); ok {
						codes[c] = fset.Position(n.Pos()).String()
					}
				}
			case *ast.CompositeLit:
				if sel, ok := n.Type.(*ast.SelectorExpr); ok && sel.Sel.Name == "Error" {
					if x, ok := sel.X.(*ast.Ident); ok && x.Name == "proto" {
						for _, el := range n.Elts {
							if kv, ok := el.(*ast.KeyValueExpr); ok {
								if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Code" {
									if c, ok := value(kv.Value); ok {
										codes[c] = fset.Position(n.Pos()).String()
									}
								}
							}
						}
					}
				}
			}
			return true
		})
	}
	if len(codes) < 20 {
		t.Fatalf("found only %d codes: the test no longer reads the host's code", len(codes))
	}
	var missing []string
	for c, at := range codes {
		if _, ok := hostKinds[c]; !ok {
			missing = append(missing, c+" ("+at+")")
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("the host code %s has no kind in hostKinds", m)
	}
}

// parseDir parses the Go files of dir, without its tests.
func parseDir(t *testing.T, dir string, fset *token.FileSet) []*ast.File {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	return files
}

func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, err == nil
}

// One code has one kind everywhere: in every UserError that the client
// builds, and in the table of the host's codes (docs/contributing/style.md).
func TestOneCodeOneKind(t *testing.T) {
	kinds := map[string]map[string]string{} // code, kind, where
	fset := token.NewFileSet()
	err := filepath.WalkDir("..", func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok || !namesUserError(lit.Type, f.Name.Name == "client") {
				return true
			}
			code, kind := "", "KindFailed"
			for _, el := range lit.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				switch k, _ := kv.Key.(*ast.Ident); {
				case k == nil:
				case k.Name == "Code":
					code, _ = stringLit(kv.Value)
				case k.Name == "Kind":
					kind = lastName(kv.Value)
				}
			}
			if code != "" {
				if kinds[code] == nil {
					kinds[code] = map[string]string{}
				}
				kinds[code][kind] = fset.Position(lit.Pos()).String()
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(kinds) < 30 {
		t.Fatalf("found only %d codes: the test no longer reads the code", len(kinds))
	}
	names := map[Kind]string{KindFailed: "KindFailed", KindUsage: "KindUsage", KindNotFound: "KindNotFound",
		KindNeedsAnswer: "KindNeedsAnswer", KindRefused: "KindRefused", KindCancelled: "KindCancelled"}
	for code, ks := range kinds {
		if len(ks) > 1 {
			t.Errorf("the code %s has %d kinds: %v", code, len(ks), ks)
		}
		if hk, ok := hostKinds[code]; ok {
			for k, at := range ks {
				if k != names[hk] {
					t.Errorf("the code %s is %s at %s, and %s from a host", code, k, at, names[hk])
				}
			}
		}
	}
}

func namesUserError(e ast.Expr, inClient bool) bool {
	switch e := e.(type) {
	case *ast.SelectorExpr:
		x, ok := e.X.(*ast.Ident)
		return ok && x.Name == "client" && e.Sel.Name == "UserError"
	case *ast.Ident:
		return inClient && e.Name == "UserError"
	}
	return false
}

// lastName is the name of an identifier, or of a selector's last part.
func lastName(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return e.Sel.Name
	}
	return ""
}
