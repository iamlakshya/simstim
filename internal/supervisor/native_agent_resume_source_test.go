package supervisor

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// This source contract guards the real backend integration without making a
// fake native scope or granting runtime rights in an unprivileged fixture.
func checkWarmBackendOwnershipSourceV1(source []byte) string {
	file, err := parser.ParseFile(token.NewFileSet(), "native_agent_resume.go", source, 0)
	if err != nil {
		return "cannot parse the actual warm backend"
	}
	var backend *ast.FuncDecl
	for _, declaration := range file.Decls {
		if fn, ok := declaration.(*ast.FuncDecl); ok && fn.Name.Name == "resumeAgentForNativeScopeV1" {
			backend = fn
		}
	}
	if backend == nil {
		return "the actual warm backend is absent"
	}
	var marker, mutation, commit token.Pos
	var checks []*ast.CallExpr
	count := 0
	ast.Inspect(backend.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		receiver, ok := selector.X.(*ast.Ident)
		if !ok {
			return true
		}
		switch {
		case receiver.Name == "scope" && selector.Sel.Name == "markWarmMutationV1":
			marker = call.Pos()
			count++
		case receiver.Name == "root" && selector.Sel.Name == "installMemberPolicy":
			mutation = call.Pos()
		case receiver.Name == "selected" && selector.Sel.Name == "recordWarmAgentV1":
			commit = call.Pos()
		case receiver.Name == "scope" && selector.Sel.Name == "checkCurrentV1":
			checks = append(checks, call)
		}
		return true
	})
	if count != 1 || mutation == 0 || marker >= mutation {
		return "the first real mutation lacks its preceding ownership marker"
	}
	if commit == 0 || mutation >= commit {
		return "the actual mutation and durable commit are out of order"
	}
	after := 0
	for _, check := range checks {
		if check.Pos() <= commit {
			continue
		}
		after++
		if len(check.Args) != 1 {
			return "the committed run has no sole original lifetime check"
		}
		lifetime, ok := check.Args[0].(*ast.SelectorExpr)
		if !ok || lifetime.Sel.Name != "lifetime" {
			return "request cancellation can revoke an already committed run"
		}
		owner, ok := lifetime.X.(*ast.Ident)
		if !ok || owner.Name != "scope" {
			return "the committed run borrowed another lifetime"
		}
	}
	if after != 1 {
		return "the durable commit lacks one original native lifetime check"
	}
	return ""
}

func TestNativeWarmBackendRetainsMutationAndCommittedLifetimeSource(t *testing.T) {
	source, err := os.ReadFile("native_agent_resume.go")
	if err != nil {
		t.Fatal(err)
	}
	if failure := checkWarmBackendOwnershipSourceV1(source); failure != "" {
		t.Fatal(failure)
	}
}

func TestNativeWarmBackendSourceGuardDetectsOwnershipRegressions(t *testing.T) {
	source, err := os.ReadFile("native_agent_resume.go")
	if err != nil {
		t.Fatal(err)
	}
	original := string(source)
	mutants := map[string]string{
		"missing mutation ownership":  strings.Replace(original, "scope.markWarmMutationV1()", "", 1),
		"late mutation ownership":     strings.Replace(strings.Replace(original, "scope.markWarmMutationV1()", "", 1), "\n\tdefer clear(environment.Env)", "\n\tscope.markWarmMutationV1()\n\tdefer clear(environment.Env)", 1),
		"cancel after durable commit": strings.Replace(original, "scope.checkCurrentV1(scope.lifetime)", "scope.checkCurrentV1(ctx)", 1),
	}
	for name, mutant := range mutants {
		t.Run(name, func(t *testing.T) {
			if mutant == original {
				t.Fatal("the source mutation did not alter the backend")
			}
			if failure := checkWarmBackendOwnershipSourceV1([]byte(mutant)); failure == "" {
				t.Fatal("an ownership regression passed the source contract")
			}
		})
	}
}
