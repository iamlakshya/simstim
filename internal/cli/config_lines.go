package cli

import (
	"slices"

	"github.com/BurntSushi/toml"
)

// The lines of a config file's keys. The TOML package gives the line of a
// syntax error, and no line for a key, so `config check` finds the line
// itself (docs/contributing/cli-spec.md 13.3). A small lexer
// (config_lex.go) reads the file once. It knows strings of each kind,
// comments, arrays and inline tables, so it records the line of each
// table, each element of an array of tables and each key, wherever the
// file sets it. A key that it did not record has no line.

// maxKeyParts is the most parts that a dotted key may have. A longer key
// is a finding, so that a hostile file cannot make the check slow.
const maxKeyParts = 64

// step is one part of a key's path: the key as the file spells it, its
// name as the struct knows it, and the element of an array of tables, or
// -1.
type step struct {
	key, name string
	at        int
}

// keyPath is where a key is in a file, such as host[2].machine.idle_stop.
type keyPath []step

// add is the path of the key k in the table at p.
func (p keyPath) add(k string) keyPath { return p.field(k, k) }

// field is the path of the key k in the table at p, which the struct knows
// by name: the TOML package matches a key to a field in any case.
func (p keyPath) field(k, name string) keyPath {
	return append(slices.Clone(p), step{k, name, -1})
}

// index is the path of element i of the array of tables at p.
func (p keyPath) index(i int) keyPath {
	q := slices.Clone(p)
	q[len(q)-1].at = i
	return q
}

// name is the path by the struct's names, without its indices, as the
// rules know it: host.machine.idle_stop.
func (p keyPath) name() string {
	names := make([]string, len(p))
	for i, s := range p {
		names[i] = s.name
	}
	return toml.Key(names).String()
}

// shown is the path as the file spells it, without its indices.
func (p keyPath) shown() string {
	keys := make([]string, len(p))
	for i, s := range p {
		keys[i] = s.key
	}
	return toml.Key(keys).String()
}

// node names one table, element or key of a file: the node that holds it,
// and its own key, or the index of an element of an array. The file itself
// is node 0. A path is a chain of nodes, so each one costs its own key and
// no more, however long the path.
type node struct {
	parent int
	key    string
	at     int
}

// tomlLines holds the line of each table, element and key of a file.
type tomlLines struct {
	nodes map[node]int
	// lines holds the line that first sets each node.
	lines []int
	// deep is the line of the first key with more than maxKeyParts parts,
	// or of the first array or inline table at more than maxNesting levels,
	// or 0. why says which.
	deep int
	why  string
}

// child is the node of the key k, or of element at when at >= 0, in the
// node parent. It makes the node when add is true, and otherwise returns
// -1 for a node that the file does not have.
func (t *tomlLines) child(parent int, k string, at int, add bool) int {
	n := node{parent, k, at}
	if id, ok := t.nodes[n]; ok || !add {
		if !ok {
			return -1
		}
		return id
	}
	id := len(t.lines)
	t.nodes[n] = id
	t.lines = append(t.lines, 0)
	return id
}

// set records the line of a node, unless an earlier line set it.
func (t *tomlLines) set(id, line int) {
	if t.lines[id] == 0 {
		t.lines[id] = line
	}
}

// find is the line of the key at p, or 0 when the file does not set it.
func (t *tomlLines) find(p keyPath) int {
	id := 0
	for _, s := range p {
		if id = t.child(id, s.key, -1, false); id < 0 {
			return 0
		}
		if s.at >= 0 {
			if id = t.child(id, "", s.at, false); id < 0 {
				return 0
			}
		}
	}
	return t.lines[id]
}
