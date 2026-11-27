package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
)

// hostRuleConfigSection documents the actual scalar union in its allowed
// named sections. Listing it as an all-sessions or saved-host key is wrong.
func hostRuleConfigSection(repo string) (string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(repo, "internal", "api", "layer_values.go"), nil, parser.ParseComments)
	if err != nil {
		return "", fmt.Errorf("parse host rule configuration: %w", err)
	}
	var sandbox *ast.StructType
	ast.Inspect(file, func(node ast.Node) bool {
		if spec, ok := node.(*ast.TypeSpec); ok && spec.Name.Name == "SandboxValues" {
			sandbox, _ = spec.Type.(*ast.StructType)
		}
		return true
	})
	if sandbox == nil {
		return "", fmt.Errorf("host rule configuration has no SandboxValues struct")
	}
	var source *ast.Field
	for _, candidate := range sandbox.Fields.List {
		if tomlKey(candidate) == "host" {
			source = candidate
			break
		}
	}
	if source == nil {
		return "", fmt.Errorf("host rule configuration has no sandbox.host field")
	}
	pointer, ok := source.Type.(*ast.StarExpr)
	if !ok {
		return "", fmt.Errorf("sandbox.host is not an optional scalar")
	}
	name, ok := pointer.X.(*ast.Ident)
	if !ok || name.Name != "HostSelectorV1" {
		return "", fmt.Errorf("sandbox.host does not use the canonical host selector")
	}
	var out strings.Builder
	out.WriteString("\n## Host rules\n\n")
	out.WriteString("Put `sandbox.host` in a named profile, a named repository section, or the session's `--set` values. It takes a host name, an ordered list of names, or `auto`. A narrower value replaces the broader choice.\n\n")
	table(&out, []field{
		{key: "profiles.\"<name>\".sandbox.host", typ: "host name, ordered list or auto", doc: plainDoc(source)},
		{key: "repos.\"<origin>\".sandbox.host", typ: "host name, ordered list or auto", doc: plainDoc(source)},
	}, false)
	out.WriteString("\nThe all-sessions layer, a saved host's layer and any lock cannot select the host. A repository's own settings file cannot select it either. A host named by the invoker always wins over a rule. See [pick a host](../how-to/pick-a-host.md) for the complete order and the rules for repository sections.\n")
	return out.String(), nil
}
