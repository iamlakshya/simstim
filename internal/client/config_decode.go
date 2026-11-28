package client

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	tomlv2 "github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

// ConfigPos is a 1-based line and column in a TOML file.
type ConfigPos struct {
	Line   int
	Column int
}

// FieldAlias records two different spellings that fold to the same known TOML
// key or table in one scope (e.g. "motion" and "Motion").
type FieldAlias struct {
	Table     string
	Canonical string
	First     string
	Second    string
	Pos       ConfigPos
}

// TOMLInspection holds AST positions and case-alias findings collected by
// walking go-toml/v2's parser without any custom TOML lexer.
type TOMLInspection struct {
	positions map[posKey]ConfigPos
	hostPos   []ConfigPos
	Aliases   []FieldAlias
}

type posKey struct {
	hostIdx int
	path    string
}

type aliasScope struct {
	hostIdx int
	table   string
	canon   string
}

// Pos returns the recorded position for a top-level or nested key in
// config.toml (or a free-map entry using key parts).
func (ins *TOMLInspection) Pos(parts ...string) ConfigPos {
	if ins == nil {
		return ConfigPos{}
	}
	return ins.positions[posKey{hostIdx: -1, path: joinPosPath(parts)}]
}

// HostPos returns the recorded position of the idx-th [[host]] entry itself,
// or of a nested key within that host entry when parts are given.
func (ins *TOMLInspection) HostPos(idx int, parts ...string) ConfigPos {
	if ins == nil {
		return ConfigPos{}
	}
	if len(parts) == 0 {
		if idx >= 0 && idx < len(ins.hostPos) {
			return ins.hostPos[idx]
		}
		return ConfigPos{}
	}
	for n := len(parts); n > 0; n-- {
		if p, ok := ins.positions[posKey{hostIdx: idx, path: joinPosPath(parts[:n])}]; ok {
			return p
		}
	}
	if idx >= 0 && idx < len(ins.hostPos) {
		return ins.hostPos[idx]
	}
	return ConfigPos{}
}

func joinPosPath(parts []string) string { return strings.Join(parts, "\x00") }

// stripUTF8BOM removes an optional leading UTF-8 BOM so go-toml/v2 accepts
// BOM-prefixed files while keeping line numbers intact.
func stripUTF8BOM(data []byte) []byte {
	return bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
}

// InspectTOML walks data with go-toml/v2's unstable.Parser to enforce the
// structural limits of cli-spec 13.1, record key positions for value checks,
// and detect case-folding aliases against rootType's struct fields.
func InspectTOML(path string, data []byte, rootType reflect.Type) (*TOMLInspection, error) {
	clean := stripUTF8BOM(data)
	var p unstable.Parser
	p.Reset(clean)

	w := &tomlWalker{
		path:      path,
		parser:    &p,
		rootType:  derefType(rootType),
		positions: map[posKey]ConfigPos{},
		seen:      map[aliasScope]map[string]ConfigPos{},
		hostIdx:   -1,
	}
	for p.NextExpression() {
		if err := w.visitExpr(p.Expression()); err != nil {
			return nil, err
		}
	}
	ins := &TOMLInspection{
		positions: w.positions,
		hostPos:   w.hostPos,
		Aliases:   w.collectAliases(),
	}
	return ins, nil
}

type tomlWalker struct {
	path          string
	parser        *unstable.Parser
	rootType      reflect.Type
	positions     map[posKey]ConfigPos
	hostPos       []ConfigPos
	seen          map[aliasScope]map[string]ConfigPos
	curTable      []string
	hostIdx       int
	totalKeyBytes int
}

func (w *tomlWalker) visitExpr(e *unstable.Node) error {
	if e == nil {
		return nil
	}
	switch e.Kind {
	case unstable.Table:
		parts, pos := w.extractKey(e.Key())
		if err := w.accountKey(parts, pos); err != nil {
			return err
		}
		w.curTable = parts
		w.recordChain(parts, pos, false)
	case unstable.ArrayTable:
		parts, pos := w.extractKey(e.Key())
		if err := w.accountKey(parts, pos); err != nil {
			return err
		}
		w.curTable = parts
		if len(parts) == 1 && strings.EqualFold(parts[0], "host") {
			w.hostIdx++
			w.hostPos = append(w.hostPos, pos)
		}
		w.recordChain(parts, pos, true)
	case unstable.KeyValue:
		parts, pos := w.extractKey(e.Key())
		full := make([]string, 0, len(w.curTable)+len(parts))
		full = append(full, w.curTable...)
		full = append(full, parts...)
		if err := w.accountKey(full, pos); err != nil {
			return err
		}
		w.recordChain(full, pos, false)
		return w.visitValue(e.Value(), full, 0)
	default:
		// Comments and other non-binding nodes need no tracking.
	}
	return nil
}

func (w *tomlWalker) visitValue(v *unstable.Node, prefix []string, depth int) error {
	if v == nil {
		return nil
	}
	switch v.Kind {
	case unstable.Array:
		depth++
		if depth > MaxConfigValueDepth {
			return w.tooLarge("value is nested more than 64 levels deep", w.nodePos(v))
		}
		isHostArr := len(prefix) == 1 && strings.EqualFold(prefix[0], "host")
		it := v.Children()
		for it.Next() {
			elem := it.Node()
			if isHostArr && elem.Kind == unstable.InlineTable {
				w.hostIdx++
				w.hostPos = append(w.hostPos, w.nodePos(elem))
			}
			if err := w.visitValue(elem, prefix, depth); err != nil {
				return err
			}
		}
	case unstable.InlineTable:
		depth++
		if depth > MaxConfigValueDepth {
			return w.tooLarge("value is nested more than 64 levels deep", w.nodePos(v))
		}
		it := v.Children()
		for it.Next() {
			kv := it.Node()
			if kv.Kind != unstable.KeyValue {
				continue
			}
			parts, pos := w.extractKey(kv.Key())
			full := make([]string, 0, len(prefix)+len(parts))
			full = append(full, prefix...)
			full = append(full, parts...)
			if err := w.accountKey(full, pos); err != nil {
				return err
			}
			w.recordChain(full, pos, false)
			if err := w.visitValue(kv.Value(), full, depth); err != nil {
				return err
			}
		}
	default:
		// Scalar values have no nested keys or containers.
	}
	return nil
}

func (w *tomlWalker) extractKey(it unstable.Iterator) ([]string, ConfigPos) {
	var parts []string
	var pos ConfigPos
	first := true
	for it.Next() {
		n := it.Node()
		if first {
			pos = w.nodePos(n)
			first = false
		}
		parts = append(parts, string(n.Data))
	}
	return parts, pos
}

func (w *tomlWalker) nodePos(n *unstable.Node) ConfigPos {
	if n == nil || n.Raw.Length == 0 && n.Raw.Offset == 0 && len(w.parser.Data()) == 0 {
		return ConfigPos{}
	}
	sh := w.parser.Shape(n.Raw)
	return ConfigPos{Line: sh.Start.Line, Column: sh.Start.Column}
}

func (w *tomlWalker) accountKey(parts []string, pos ConfigPos) error {
	if len(parts) > MaxConfigKeyParts {
		return w.tooLarge("key has more than 64 parts", pos)
	}
	keyLen := 0
	for i, part := range parts {
		if i > 0 {
			keyLen++
		}
		keyLen += len(part)
	}
	w.totalKeyBytes += keyLen
	if w.totalKeyBytes > MaxConfigKeyBytes {
		return w.tooLarge("key names add up to more than 8 MiB", pos)
	}
	return nil
}

func (w *tomlWalker) tooLarge(msg string, pos ConfigPos) error {
	return &ConfigReadError{
		Path:   w.path,
		Code:   "too_large",
		Msg:    msg,
		Line:   pos.Line,
		Column: pos.Column,
	}
}

// recordChain walks rootType along parts, recording canonical positions and
// detecting case-folding aliases on known struct fields without folding free
// maps.
func (w *tomlWalker) recordChain(parts []string, pos ConfigPos, isArrayTable bool) {
	t := w.rootType
	canon := make([]string, 0, len(parts))
	scopeHost := -1
	for i, part := range parts {
		t = derefType(t)
		if t == nil {
			canon = append(canon, part)
			continue
		}
		if t.Kind() == reflect.Slice {
			t = derefType(t.Elem())
		}
		if t.Kind() == reflect.Map {
			// Free maps keep exact keys; never fold or alias-check their entries.
			canon = append(canon, parts[i:]...)
			break
		}
		if t.Kind() != reflect.Struct {
			canon = append(canon, part)
			t = nil
			continue
		}
		tag, ft, ok := matchStructField(t, part)
		if !ok {
			canon = append(canon, part)
			t = nil
			continue
		}
		// Record spelling for case-folding alias checks.
		tableName := strings.Join(canon, ".")
		sc := aliasScope{hostIdx: scopeHost, table: tableName, canon: tag}
		if w.seen[sc] == nil {
			w.seen[sc] = map[string]ConfigPos{}
		}
		if _, exists := w.seen[sc][part]; !exists {
			w.seen[sc][part] = pos
		}
		canon = append(canon, tag)
		if i == 0 && tag == "host" && w.hostIdx >= 0 {
			scopeHost = w.hostIdx
		}
		t = ft
		if isArrayTable && i == len(parts)-1 {
			break
		}
	}
	if len(canon) == 0 {
		return
	}
	keyParts := canon
	hIdx := -1
	if canon[0] == "host" && w.hostIdx >= 0 {
		hIdx = w.hostIdx
		keyParts = canon[1:]
	}
	if len(keyParts) > 0 {
		k := posKey{hostIdx: hIdx, path: joinPosPath(keyParts)}
		if _, exists := w.positions[k]; !exists {
			w.positions[k] = pos
		}
	}
}

func (w *tomlWalker) collectAliases() []FieldAlias {
	var out []FieldAlias
	for sc, spellings := range w.seen {
		if len(spellings) < 2 {
			continue
		}
		names := make([]string, 0, len(spellings))
		for s := range spellings {
			names = append(names, s)
		}
		sort.Strings(names)
		// Spec 13.2: report both spellings and the line of the second key in
		// sorted order.
		out = append(out, FieldAlias{
			Table:     sc.table,
			Canonical: sc.canon,
			First:     names[0],
			Second:    names[1],
			Pos:       spellings[names[1]],
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Pos.Line != out[j].Pos.Line {
			return out[i].Pos.Line < out[j].Pos.Line
		}
		if out[i].Table != out[j].Table {
			return out[i].Table < out[j].Table
		}
		return out[i].First < out[j].First
	})
	return out
}

// DecodeConfigStrict decodes TOML bytes into dst using go-toml/v2's
// DisallowUnknownFields, after running structural bounds and alias inspection.
// If only unknown keys were encountered, strictErr is non-nil, dst holds all
// decoded known fields, and err is nil.
func DecodeConfigStrict(path string, data []byte, dst any) (strictErr *tomlv2.StrictMissingError, ins *TOMLInspection, err error) {
	ins, err = InspectTOML(path, data, reflect.TypeOf(dst))
	if err != nil {
		return nil, nil, err
	}
	clean := stripUTF8BOM(data)
	dec := tomlv2.NewDecoder(bytes.NewReader(clean)).DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var sme *tomlv2.StrictMissingError
		if errors.As(err, &sme) {
			return sme, ins, nil
		}
		return nil, ins, fmt.Errorf("decode %s: %w", path, err)
	}
	return nil, ins, nil
}

// DecodeConfigLoose decodes TOML bytes into dst using go-toml/v2 without
// failing on unknown keys, while still enforcing structural limits and
// case-folding alias rejection.
func DecodeConfigLoose(path string, data []byte, dst any) (*TOMLInspection, error) {
	ins, err := InspectTOML(path, data, reflect.TypeOf(dst))
	if err != nil {
		return nil, err
	}
	clean := stripUTF8BOM(data)
	if err := tomlv2.NewDecoder(bytes.NewReader(clean)).Decode(dst); err != nil {
		return ins, fmt.Errorf("decode %s: %w", path, err)
	}
	return ins, nil
}
