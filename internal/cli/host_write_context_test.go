package cli

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
)

// This guard covers direct canonical writer calls. Runtime tests cover the
// transaction boundaries; the guard does not claim general context dataflow.
func TestProductionHostWritesKeepTheirContext(t *testing.T) {
	root := filepath.Join("..", "..")
	cmd := exec.CommandContext(t.Context(), "git", "-c", "core.fsmonitor=false", "ls-files", "-co", "--exclude-standard", "-z", "--", "*.go")
	cmd.Dir = root
	paths, err := cmd.Output()
	if err != nil {
		t.Fatalf("list repository Go source: %v", err)
	}
	for _, rel := range strings.Split(string(paths), "\x00") {
		if rel == "" || strings.HasSuffix(rel, "_test.go") {
			continue
		}
		fileSet := token.NewFileSet()
		file, err := parser.ParseFile(fileSet, filepath.Join(root, rel), nil, 0)
		if err != nil {
			t.Fatalf("parse host writer calls: %v", err)
		}
		for _, call := range detachedHostWrites(file, strings.HasPrefix(filepath.ToSlash(rel), "internal/client/")) {
			t.Errorf("%s: host write drops the caller's context", fileSet.Position(call.Pos()))
		}
	}
}

func detachedHostWrites(file *ast.File, clientPackage bool) []*ast.CallExpr {
	clientPath := reflect.TypeFor[client.Host]().PkgPath()
	clients, contexts := map[string]bool{}, map[string]bool{}
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		name := filepath.Base(path)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		switch path {
		case clientPath:
			clients[name] = true
		case "context":
			contexts[name] = true
		}
	}
	var bad []*ast.CallExpr
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		fnClients, fnContexts := visibleHostImports(clients, fn), visibleHostImports(contexts, fn)
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := canonicalHostCall(call, fnClients, clientPackage)
			if !hostWriterName(name) || compatibilityHostAdapter(file.Name.Name, fn, call, name, fnClients, fnContexts) {
				return true
			}
			if name == "SaveHost" || name == "SaveHostPin" || name == "ForgetHost" ||
				(len(call.Args) > 0 && detachedContext(call.Args[0], fnContexts)) {
				bad = append(bad, call)
			}
			return true
		})
	}
	return bad
}

func visibleHostImports(imports map[string]bool, fn *ast.FuncDecl) map[string]bool {
	visible := make(map[string]bool, len(imports))
	for name, imported := range imports {
		visible[name] = imported
	}
	for _, fields := range []*ast.FieldList{fn.Type.Params, fn.Type.TypeParams, fn.Recv} {
		if fields == nil {
			continue
		}
		for _, field := range fields.List {
			for _, name := range field.Names {
				delete(visible, name.Name)
			}
		}
	}
	return visible
}

func canonicalHostCall(call *ast.CallExpr, clients map[string]bool, clientPackage bool) string {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		if clientPackage {
			return fun.Name
		}
	case *ast.SelectorExpr:
		if pkg, ok := fun.X.(*ast.Ident); ok && clients[pkg.Name] {
			return fun.Sel.Name
		}
	}
	return ""
}

func hostWriterName(name string) bool {
	return name == "UpdateHosts" || strings.HasPrefix(name, "SaveHost") || strings.HasPrefix(name, "ForgetHost")
}

func detachedContext(expr ast.Expr, contexts map[string]bool) bool {
	detached := false
	ast.Inspect(expr, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := selector.X.(*ast.Ident)
		if ok && contexts[pkg.Name] {
			detached = detached || selector.Sel.Name == "Background" || selector.Sel.Name == "TODO" || selector.Sel.Name == "WithoutCancel"
		}
		return true
	})
	return detached
}

// Legacy public adapters have one exact forwarding return. Their definitions
// remain compatible; an ordinary production caller cannot invoke them.
func compatibilityHostAdapter(pkg string, fn *ast.FuncDecl, call *ast.CallExpr, target string, clients, contexts map[string]bool) bool {
	if len(fn.Body.List) != 1 || fn.Type.Params.NumFields() != 1 {
		return false
	}
	ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 || ret.Results[0] != call || len(fn.Type.Params.List[0].Names) != 1 {
		return false
	}
	param := fn.Type.Params.List[0].Names[0].Name
	if pkg == "client" && fn.Recv == nil &&
		(fn.Name.Name == "SaveHost" || fn.Name.Name == "SaveHostPin" || fn.Name.Name == "ForgetHost") {
		if target != fn.Name.Name+"Context" || len(call.Args) != 2 || !detachedContext(call.Args[0], contexts) {
			return false
		}
		arg, ok := call.Args[1].(*ast.Ident)
		root, rootOK := call.Args[0].(*ast.CallExpr)
		selector, selectorOK := func() (*ast.SelectorExpr, bool) {
			if !rootOK {
				return nil, false
			}
			selector, ok := root.Fun.(*ast.SelectorExpr)
			return selector, ok
		}()
		return ok && arg.Name == param && selectorOK && selector.Sel.Name == "Background" && len(root.Args) == 0
	}
	if pkg == "tui" && fn.Name.Name == "SaveHostPin" && fn.Recv != nil && len(fn.Recv.List) == 1 && target == "SaveHostPin" && len(call.Args) == 1 {
		receiver, ok := fn.Recv.List[0].Type.(*ast.Ident)
		arg, argOK := call.Args[0].(*ast.Ident)
		return ok && receiver.Name == "realBackend" && argOK && arg.Name == param && canonicalHostCall(call, clients, false) == target
	}
	return false
}

func TestHostWriteGuardRecognisesActualCallsAndAdapters(t *testing.T) {
	clientPath := reflect.TypeFor[client.Host]().PkgPath()
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"forwarded", `func f(ctx cx.Context, h api.Host) { api.SaveHostContext(ctx,h) }`, 0},
		{"root", `func f(h api.Host) { api.SaveHostContext(cx.Background(),h) }`, 1},
		{"todo", `func f(h api.Host) { api.UpdateHosts(cx.TODO(),api.HostChange{Now:&h}) }`, 1},
		{"nested", `func f(ctx cx.Context,h api.Host) { api.UpdateHosts(cx.WithValue(cx.WithoutCancel(ctx),1,2),api.HostChange{Now:&h}) }`, 1},
		{"contextless", `func f(h api.Host) { api.SaveHost(h) }`, 1},
		{"shadowed import", `func f(api interface{ SaveHost(any) }) { api.SaveHost(nil) }`, 0},
		{"comment", `// api.SaveHost(h)
func f() {}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := fmt.Sprintf("package fixture\nimport (api %q; cx \"context\")\n%s", clientPath, tc.body)
			file, err := parser.ParseFile(token.NewFileSet(), tc.name, src, 0)
			if err != nil {
				t.Fatal(err)
			}
			if got := len(detachedHostWrites(file, false)); got != tc.want {
				t.Fatalf("direct detached calls = %d, want %d", got, tc.want)
			}
		})
	}
	file, err := parser.ParseFile(token.NewFileSet(), "adapter", `package client
import "context"
func SaveHost(h Host) error { return SaveHostContext(context.Background(),h) }
func ordinary(h Host) { SaveHost(h) }
`, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(detachedHostWrites(file, true)); got != 1 {
		t.Fatalf("legacy definition and real caller produced %d violations, want only the caller", got)
	}
}
