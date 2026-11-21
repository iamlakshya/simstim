package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
)

// commentedConfigFields bridges only the requested source-owned struct fields
// into the existing table writer. It does not expand embedded policy schemas.
func commentedConfigFields(repo, path, typeLabel, prefix string, keys ...string) ([]field, error) {
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(repo, filepath.FromSlash(path)), nil, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse configuration fields in %s: %w", path, err)
	}
	var values *ast.StructType
	ast.Inspect(file, func(node ast.Node) bool {
		if spec, ok := node.(*ast.TypeSpec); ok && spec.Name.Name == typeLabel {
			values, _ = spec.Type.(*ast.StructType)
		}
		return true
	})
	if values == nil {
		return nil, fmt.Errorf("configuration source %s has no %s struct", path, typeLabel)
	}
	wanted := map[string]bool{}
	for _, key := range keys {
		wanted[key] = true
	}
	var out []field
	for _, source := range values.Fields.List {
		key := tomlKey(source)
		if key == "" || key == "-" || len(keys) > 0 && !wanted[key] {
			continue
		}
		typ := source.Type
		if pointer, ok := typ.(*ast.StarExpr); ok {
			typ = pointer.X
		}
		out = append(out, field{key: prefix + "." + key, typ: typeName(typ), doc: plainDoc(source)})
		delete(wanted, key)
	}
	if len(wanted) > 0 {
		return nil, fmt.Errorf("configuration source %s is missing a requested field", path)
	}
	return out, nil
}
