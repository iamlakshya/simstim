package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// field is one key of a config file, read from a struct's source.
type field struct {
	key, typ, doc, def string
}

// configPage documents config.toml and hosts.toml from the comments on the
// Settings, Host, Provider and Machine structs in internal/client.
func configPage(repo string) ([]byte, error) {
	fset := token.NewFileSet()
	types := map[string]*ast.StructType{}
	for _, f := range []string{"config.go", "caches.go", "provider.go", "machine.go"} {
		file, err := parser.ParseFile(fset, filepath.Join(repo, "internal", "client", f), nil, parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", f, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if ts, ok := n.(*ast.TypeSpec); ok {
				if st, ok := ts.Type.(*ast.StructType); ok {
					types[ts.Name.Name] = st
				}
			}
			return true
		})
	}
	for _, want := range []string{"Settings", "Host", "Provider", "Machine"} {
		if types[want] == nil {
			return nil, fmt.Errorf("no struct %s in internal/client", want)
		}
	}
	defaults := flatten(reflect.ValueOf(client.DefaultSettings()), "")
	settings := fields(types["Settings"], "", types, defaults)
	hosts := fields(types["Host"], "", types, nil)

	var b strings.Builder
	b.WriteString(header("Configuration", "2"))
	fmt.Fprintf(&b, "%s reads two files from `~/.config/%s/` (or `$XDG_CONFIG_HOME/%s/`). `%s config path` prints where they are, and `%s config check` checks both files for mistakes.\n\n", brand.DisplayName, brand.Name, brand.Name, brand.Name, brand.Name)
	b.WriteString("## config.toml\n\nYour preferences. Every key is optional; a missing key takes its default.\n\n")
	table(&b, settings, true)
	b.WriteString("\n## hosts.toml\n\nThe hosts you added. `host add` writes it, and you can edit it by hand. Each host is a `[[host]]` table.\n\n")
	table(&b, hosts, false)
	return []byte(b.String()), nil
}

// fields lists a struct's keys, depth first, with nested tables as
// "section.key". Map fields are listed as tables of their own.
func fields(st *ast.StructType, prefix string, types map[string]*ast.StructType, defaults map[string]string) []field {
	var out []field
	for _, f := range st.Fields.List {
		key := tomlKey(f)
		if key == "" || key == "-" {
			continue
		}
		full := key
		if prefix != "" {
			full = prefix + "." + key
		}
		doc := plainDoc(f)
		switch t := f.Type.(type) {
		case *ast.StructType:
			out = append(out, fields(t, full, types, defaults)...)
			continue
		case *ast.StarExpr:
			if id, ok := t.X.(*ast.Ident); ok && types[id.Name] != nil {
				out = append(out, field{key: "[" + full + "]", typ: "table", doc: doc})
				out = append(out, fields(types[id.Name], full, types, defaults)...)
				continue
			}
		}
		out = append(out, field{key: full, typ: typeName(f.Type), doc: doc, def: defaults[full]})
	}
	return out
}

func tomlKey(f *ast.Field) string {
	if f.Tag == nil {
		return ""
	}
	tag := reflect.StructTag(strings.Trim(f.Tag.Value, "`")).Get("toml")
	return strings.Split(tag, ",")[0]
}

func typeName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return typeName(t.X)
	case *ast.Ident:
		switch t.Name {
		case "string":
			return "string"
		case "bool":
			return "boolean"
		case "int", "int64":
			return "integer"
		}
		return t.Name
	case *ast.MapType:
		return "table"
	case *ast.ArrayType:
		return "array of " + typeName(t.Elt)
	}
	return "value"
}

// flatten turns a settings value into "section.key" = default.
func flatten(v reflect.Value, prefix string) map[string]string {
	out := map[string]string{}
	t := v.Type()
	for i := range t.NumField() {
		key := strings.Split(t.Field(i).Tag.Get("toml"), ",")[0]
		if key == "" {
			continue
		}
		if prefix != "" {
			key = prefix + "." + key
		}
		fv := v.Field(i)
		switch fv.Kind() {
		case reflect.Struct:
			for k, d := range flatten(fv, key) {
				out[k] = d
			}
		case reflect.String:
			if fv.String() != "" {
				out[key] = "`\"" + fv.String() + "\"`"
			}
		case reflect.Bool:
			out[key] = fmt.Sprintf("`%v`", fv.Bool())
		case reflect.Pointer:
			if !fv.IsNil() {
				elem := fv.Elem()
				switch elem.Kind() {
				case reflect.Bool:
					out[key] = fmt.Sprintf("`%v`", elem.Bool())
				case reflect.String:
					if elem.String() != "" {
						out[key] = "`\"" + elem.String() + "\"`"
					}
				default:
					// Other pointer targets have no default worth showing.
				}
			}
		default:
			// Other kinds have no default worth showing.
		}
	}
	return out
}

func table(b *strings.Builder, fs []field, withDefault bool) {
	if withDefault {
		b.WriteString("| Key | Type | Default | What it does |\n| --- | --- | --- | --- |\n")
	} else {
		b.WriteString("| Key | Type | What it does |\n| --- | --- | --- |\n")
	}
	for _, f := range fs {
		def := f.def
		if def == "" {
			def = "—"
		}
		doc := strings.ReplaceAll(f.doc, "|", "\\|")
		if withDefault {
			fmt.Fprintf(b, "| `%s` | %s | %s | %s |\n", f.key, f.typ, def, doc)
		} else {
			fmt.Fprintf(b, "| `%s` | %s | %s |\n", f.key, f.typ, doc)
		}
	}
}

// plainDoc is a field's comment as a sentence for users: Go's "Motion turns
// the animation on" becomes "Turns the animation on".
func plainDoc(f *ast.Field) string {
	words := strings.Fields(f.Doc.Text())
	if len(words) > 1 && len(f.Names) == 1 && words[0] == f.Names[0].Name {
		words = words[1:]
		if r := []rune(words[0]); len(r) > 0 {
			words[0] = strings.ToUpper(string(r[0])) + string(r[1:])
		}
	}
	return strings.Join(words, " ")
}
