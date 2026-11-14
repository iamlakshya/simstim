// Package configdata shares the typed TOML schema and alias rules between
// client settings, their checker and trusted-side repository policy reads.
package configdata

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// ConfigKey is one TOML key, its spelling and the struct field it sets.
// Type is nil for an unknown key. Conflict names two spellings of one
// field when this key follows the first spelling in sorted order.
type ConfigKey struct {
	Spelling, Name, Conflict string
	Type                     reflect.Type
}

// ConfigTable is the known fields and sorted keys of one TOML table.
type ConfigTable struct {
	Fields map[string]reflect.Type
	Keys   []ConfigKey
}

// InspectConfigTable matches the keys of table to typ's TOML tags, using
// exact matches before case-insensitive matches, as the TOML decoder does.
// It marks aliases of one field as conflicts. It never folds values or
// free-map keys. The checker and the ordinary loaders share this rule.
func InspectConfigTable(table map[string]any, typ reflect.Type) ConfigTable {
	out := ConfigTable{Fields: map[string]reflect.Type{}}
	fields := configFields(typ)
	for _, field := range fields {
		out.Fields[field.name] = field.typ
	}
	seen := map[string]string{}
	for _, spelling := range slices.Sorted(maps.Keys(table)) {
		name := spelling
		if _, exact := out.Fields[name]; !exact {
			for _, candidate := range fields {
				if strings.EqualFold(candidate.name, spelling) {
					name = candidate.name
					break
				}
			}
		}
		key := ConfigKey{Spelling: spelling, Name: name, Type: out.Fields[name]}
		if key.Type != nil {
			if first, duplicate := seen[name]; duplicate {
				key.Conflict = fmt.Sprintf("both %q and %q set %q", first, spelling, name)
			} else {
				seen[name] = spelling
			}
		}
		out.Keys = append(out.Keys, key)
	}
	return out
}

// configField is a schema field in the decoder's declaration order.
type configField struct {
	name   string
	typ    reflect.Type
	index  []int
	tagged bool
}

// configFields applies the pinned TOML decoder's embedding rules: the
// shallowest fields win, a single tagged field wins a tie, and other ties
// name no field. Case-insensitive fallback follows declaration order.
func configFields(typ reflect.Type) []configField {
	var found []configField
	collectConfigFields(typ, nil, map[reflect.Type]bool{}, &found)
	groups := map[string][]configField{}
	for _, field := range found {
		groups[field.name] = append(groups[field.name], field)
	}
	var fields []configField
	for _, group := range groups {
		if field, ok := dominantConfigField(group); ok {
			fields = append(fields, field)
		}
	}
	slices.SortFunc(fields, func(a, b configField) int { return slices.Compare(a.index, b.index) })
	return fields
}

// collectConfigFields expands only anonymous, untagged structs. The
// active types stop recursive embeddings without merging distinct paths.
func collectConfigFields(typ reflect.Type, path []int, active map[reflect.Type]bool, fields *[]configField) {
	if active[typ] {
		return
	}
	active[typ] = true
	defer delete(active, typ)
	for i := range typ.NumField() {
		field := typ.Field(i)
		tag := field.Tag.Get("toml")
		if field.PkgPath != "" && !field.Anonymous || tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		index := append(slices.Clone(path), i)
		child := field.Type
		if child.Kind() == reflect.Pointer {
			child = child.Elem()
		}
		if field.Anonymous && name == "" && child.Kind() == reflect.Struct {
			collectConfigFields(child, index, active, fields)
			continue
		}
		tagged := name != ""
		if name == "" {
			name = field.Name
		}
		*fields = append(*fields, configField{name: name, typ: field.Type, index: index, tagged: tagged})
	}
}

// dominantConfigField resolves one exact field name independently of any
// input spelling. An unresolved tie is an unknown key to the decoder.
func dominantConfigField(group []configField) (configField, bool) {
	depth := len(group[0].index)
	for _, field := range group[1:] {
		depth = min(depth, len(field.index))
	}
	var winner configField
	count, tagged := 0, 0
	for _, field := range group {
		if len(field.index) != depth {
			continue
		}
		count++
		if field.tagged {
			tagged++
			winner = field
		} else if winner.typ == nil {
			winner = field
		}
	}
	return winner, tagged == 1 || tagged == 0 && count == 1
}

// Decode refuses aliases and wrong known map containers before the typed
// decoder can overwrite or discard a field. Unknown keys keep their policy.
// Both parses use the same bounded byte string, not another file read.
func Decode(data string, dst any) (toml.MetaData, error) {
	meta, _, err := DecodeTable(data, dst)
	return meta, err
}

// DecodeTable returns the same parsed table used by the alias preflight.
// Callers can restrict a schema subset without another parse or field table.
func DecodeTable(data string, dst any) (toml.MetaData, map[string]any, error) {
	var table map[string]any
	meta, err := toml.Decode(data, &table)
	if err != nil {
		return meta, nil, err
	}
	var shapeErr error
	if err := configAliases(table, reflect.TypeOf(dst).Elem(), nil, &shapeErr); err != nil {
		return meta, nil, err
	}
	if shapeErr != nil {
		return meta, nil, shapeErr
	}
	meta, err = toml.Decode(data, dst)
	return meta, table, err
}

// AliasError identifies an ambiguous schema key without pretending
// that the decoder has a line for it. The checker supplies that line.
type AliasError struct{ message string }

// Error returns the ambiguous-key message.
func (e *AliasError) Error() string { return e.message }

// configAliases walks the actual schema once. Alias errors retain priority.
// The first map-shape error waits until every possible alias was checked.
// Unknown subtrees and scalar free-map values keep their existing policy.
func configAliases(value any, typ reflect.Type, path toml.Key, shapeErr *error) error {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Struct:
		return configTableAliases(value, typ, path, shapeErr)
	case reflect.Slice, reflect.Array:
		return configArrayAliases(value, typ.Elem(), path, shapeErr)
	case reflect.Map:
		_, table := value.(map[string]any)
		if err := CheckMapShape(typ, table); err != nil {
			if *shapeErr == nil {
				*shapeErr = fmt.Errorf("%s %w", path.String(), err)
			}
			return nil // Continue aliases elsewhere before reporting shape.
		}
		return configMapAliases(value, typ.Elem(), path, shapeErr)
	default:
		return nil
	}
}

// configTableAliases checks one known table and its known child fields.
func configTableAliases(value any, typ reflect.Type, path toml.Key, shapeErr *error) error {
	table, ok := value.(map[string]any)
	if !ok {
		return nil // The typed decoder reports a wrong type.
	}
	for _, key := range InspectConfigTable(table, typ).Keys {
		if key.Conflict != "" {
			where := ""
			if len(path) > 0 {
				where = " in [" + path.String() + "]"
			}
			return &AliasError{message: key.Conflict + where}
		}
		if key.Type != nil {
			here := append(slices.Clone(path), key.Spelling)
			if err := configAliases(table[key.Spelling], key.Type, here, shapeErr); err != nil {
				return err
			}
		}
	}
	return nil
}

// configArrayAliases gives each known container element its own scope.
// Scalar elements keep their existing native type checks.
func configArrayAliases(value any, elem reflect.Type, path toml.Key, shapeErr *error) error {
	for elem.Kind() == reflect.Pointer {
		elem = elem.Elem()
	}
	if !configHasContainers(elem) {
		return nil
	}
	switch values := value.(type) {
	case []map[string]any:
		for _, v := range values {
			if err := configAliases(v, elem, path, shapeErr); err != nil {
				return err
			}
		}
	case []any:
		for _, v := range values {
			if err := configAliases(v, elem, path, shapeErr); err != nil {
				return err
			}
		}
	}
	return nil
}

// configMapAliases walks known containers, preserving map keys as written.
// Free scalar and interface values have no child schema to inspect.
func configMapAliases(value any, elem reflect.Type, path toml.Key, shapeErr *error) error {
	for elem.Kind() == reflect.Pointer {
		elem = elem.Elem()
	}
	if !configHasContainers(elem) {
		return nil
	}
	table, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	for _, key := range slices.Sorted(maps.Keys(table)) {
		here := append(slices.Clone(path), key)
		if err := configAliases(table[key], elem, here, shapeErr); err != nil {
			return err
		}
	}
	return nil
}

// MaxFileSize is the common bound for user and repository TOML configuration.
const MaxFileSize = 1 << 20

// Field describes one actual TOML schema field in declaration order.
type Field struct {
	Name   string
	Type   reflect.Type
	Index  []int
	Tagged bool
}

// Fields exposes the same resolved field list used by Decode and inspection.
// It is a schema view, not an independent matching rule.
func Fields(typ reflect.Type) []Field {
	var out []Field
	for _, field := range configFields(typ) {
		out = append(out, Field{Name: field.name, Type: field.typ, Index: slices.Clone(field.index), Tagged: field.tagged})
	}
	return out
}

// configHasContainers limits traversal to values that can hold known fields
// or maps. Their scalar and interface leaves stay with the native decoder.
func configHasContainers(typ reflect.Type) bool {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Struct, reflect.Map, reflect.Slice, reflect.Array:
		return true
	default:
		return false
	}
}
