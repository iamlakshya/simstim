package cli

import (
	"fmt"
	"strconv"
	"strings"
)

// The lexer of `config check`: it reads a TOML file once, and records the
// line of each table, element and key (docs/contributing/cli-spec.md
// 13.3). Its time and its memory grow with the size of the file, and no
// faster: each node costs its own key, and the depth of a value, the parts
// of a key and the full names of all keys have limits.

// maxNesting is the most levels of arrays and inline tables in a value. A
// deeper value is a finding, so that a hostile file cannot make the check
// slow.
const maxNesting = 64

// maxKeyNames bounds the full names of a file's keys, added up: each key
// and each table that a key or a header makes, with the names of the
// tables that hold it. The TOML package keeps a string of that length for
// each, so a long table name with many keys under it would cost far more
// memory than the file's size. A larger total is a finding.
const maxKeyNames = 8 << 20

// lexer reads a TOML file once, and records the line of each node.
type lexer struct {
	s     string
	i     int
	line  int
	t     *tomlLines
	depth int
	// names adds up the full names of the keys so far (maxKeyNames).
	names int
	// arrays counts the elements so far of each array of tables that a
	// [[header]] opens, by the node of the array.
	arrays map[int]int
}

// place is where a key or a value is: the node of its table, whether the
// lexer records lines there, and the length of the table's full name.
type place struct {
	id   int
	rec  bool
	name int
}

// scanTOML records the line of each table, element and key in data. It
// reads a file with a mistake as well as it can. It stops at the first key
// or value that is too deep or too long, which is a finding of its own.
func scanTOML(data string) *tomlLines {
	t := &tomlLines{nodes: map[node]int{}, lines: []int{0}}
	l := &lexer{s: data, line: 1, t: t, arrays: map[int]int{}}
	// The TOML package skips a byte order mark, as UTF-8 or UTF-16.
	switch {
	case strings.HasPrefix(data, "\xef\xbb\xbf"):
		l.i = 3
	case strings.HasPrefix(data, "\xff\xfe"), strings.HasPrefix(data, "\xfe\xff"):
		l.i = 2
	}
	table := place{rec: true}
	for {
		l.blank()
		if l.i >= len(l.s) {
			return t
		}
		start := l.i
		if l.s[l.i] == '[' {
			table = l.header()
		} else {
			l.keyValue(table)
		}
		if l.i == start {
			l.i++
		}
		l.endLine()
	}
}

// tooDeep records the line of a key or a value that is too deep or too
// long, and why, and ends the scan.
func (l *lexer) tooDeep(line int, why string) {
	if l.t.deep == 0 {
		l.t.deep, l.t.why = line, why
	}
	l.i = len(l.s)
}

// named adds the full names of keys, in a table whose name has the length
// name, to the file's total, and returns the length of the last one. Each
// part of a dotted key makes a table, with a name of its own.
func (l *lexer) named(name int, keys []string, line int) int {
	for _, k := range keys {
		name += len(k) + 1
		l.names += name
	}
	if l.names > maxKeyNames {
		l.tooDeep(line, "the full names of its keys add up to more than 8 MiB")
	}
	return name
}

// header reads a header, [a.b] or [[a.b]], and returns the table that it
// opens.
func (l *lexer) header() place {
	line, end := l.line, "]"
	if strings.HasPrefix(l.s[l.i:], "[[") {
		end = "]]"
	}
	l.i += len(end)
	keys, ok := l.keys(line)
	l.spaces()
	if !ok || !strings.HasPrefix(l.s[l.i:], end) {
		return place{rec: true}
	}
	l.i += len(end)
	at := place{rec: true, name: l.named(0, keys, line)}
	for j, k := range keys {
		at.id = l.t.child(at.id, k, -1, true)
		if j == len(keys)-1 && end == "]]" {
			l.t.set(at.id, line)
			l.arrays[at.id]++
		}
		if n, ok := l.arrays[at.id]; ok {
			at.id = l.t.child(at.id, "", n-1, true)
		}
		l.t.set(at.id, line)
	}
	return at
}

// keyValue reads `key = value` in the table at, and records the key's line
// when at.rec is true. It reports whether it read a key and an =.
func (l *lexer) keyValue(at place) bool {
	line := l.line
	keys, ok := l.keys(line)
	l.spaces()
	if !ok || l.i >= len(l.s) || l.s[l.i] != '=' {
		return false
	}
	l.i++
	l.spaces()
	at.name = l.named(at.name, keys, line)
	for _, k := range keys {
		if !at.rec {
			break
		}
		at.id = l.t.child(at.id, k, -1, true)
		l.t.set(at.id, line)
	}
	l.value(at)
	return true
}

// value reads one value of the key at.
func (l *lexer) value(at place) {
	rest := l.s[l.i:]
	switch {
	case rest == "":
	case strings.HasPrefix(rest, `"""`):
		l.multiline(`"""`)
	case strings.HasPrefix(rest, "'''"):
		l.multiline("'''")
	case rest[0] == '"' || rest[0] == '\'':
		l.str(rest[0])
	case rest[0] == '{' || rest[0] == '[':
		if l.depth >= maxNesting {
			l.tooDeep(l.line, fmt.Sprintf("a value has more than %d levels of arrays and inline tables", maxNesting))
			return
		}
		l.depth++
		if rest[0] == '{' {
			l.inline(at)
		} else {
			l.array(at)
		}
		l.depth--
	default:
		// A number, a boolean or a date: up to the end of the value.
		for l.i < len(l.s) && !strings.ContainsRune(",]}#\n", rune(l.s[l.i])) {
			l.i++
		}
	}
}

// inline reads an inline table. Its keys may span lines, as TOML 1.1
// allows.
func (l *lexer) inline(at place) {
	l.i++
	for {
		l.blank()
		switch {
		case l.i >= len(l.s):
			return
		case l.s[l.i] == '}':
			l.i++
			return
		case l.s[l.i] == ',':
			l.i++
		case !l.keyValue(at):
			return
		}
	}
}

// array reads an array. An inline table in it is an element of an array
// of tables, with a line of its own. Nothing asks for the line of anything
// else in an array.
func (l *lexer) array(at place) {
	l.i++
	for n := 0; ; n++ {
		l.blank()
		if l.i < len(l.s) && l.s[l.i] == ',' {
			l.i++
			l.blank()
		}
		if l.i >= len(l.s) || l.s[l.i] == ']' {
			if l.i < len(l.s) {
				l.i++
			}
			return
		}
		el := place{rec: at.rec && l.s[l.i] == '{', name: at.name}
		if el.rec {
			el.id = l.t.child(at.id, "", n, true)
			l.t.set(el.id, l.line)
		}
		start := l.i
		l.value(el)
		if l.i == start {
			return
		}
	}
}

// str reads a string on one line, quoted with q: a basic string, with its
// escapes, or a literal one.
func (l *lexer) str(q byte) {
	for l.i++; l.i < len(l.s) && l.s[l.i] != '\n'; l.i++ {
		switch {
		case q == '"' && l.s[l.i] == '\\':
			l.i++
		case l.s[l.i] == q:
			l.i++
			return
		}
	}
}

// multiline reads a multi-line string, which ends with q, or with one or
// two more quotes before q.
func (l *lexer) multiline(q string) {
	for l.i += len(q); l.i < len(l.s); l.i++ {
		switch c := l.s[l.i]; {
		case c == '\n':
			l.line++
		case c == '\\' && q == `"""`:
			if l.i+1 < len(l.s) && l.s[l.i+1] == '\n' {
				l.line++
			}
			l.i++
		case strings.HasPrefix(l.s[l.i:], q):
			l.i += len(q)
			for extra := 0; extra < 2 && l.i < len(l.s) && l.s[l.i] == q[0]; extra++ {
				l.i++
			}
			return
		}
	}
}

// keys reads a dotted key, such as ui.motion or "session: new", on the
// line line. A key of more than maxKeyParts parts ends the scan.
func (l *lexer) keys(line int) ([]string, bool) {
	var keys []string
	for {
		l.spaces()
		k, n, ok := oneKey(l.s[l.i:])
		if !ok {
			return nil, false
		}
		l.i += n
		if keys = append(keys, k); len(keys) > maxKeyParts {
			l.tooDeep(line, fmt.Sprintf("a key has more than %d parts", maxKeyParts))
			return nil, false
		}
		l.spaces()
		if l.i >= len(l.s) || l.s[l.i] != '.' {
			return keys, true
		}
		l.i++
	}
}

// oneKey reads one part of a dotted key at the start of s: a bare key, a
// basic string or a literal string, on one line. n is its length.
func oneKey(s string) (key string, n int, ok bool) {
	if s == "" {
		return "", 0, false
	}
	switch q := s[0]; q {
	case '"', '\'':
		for n = 1; n < len(s) && s[n] != '\n'; n++ {
			switch {
			case q == '"' && s[n] == '\\':
				n++
			case s[n] == q:
				if q == '\'' {
					return s[1:n], n + 1, true
				}
				return unescape(s[1:n]), n + 1, true
			}
		}
		return "", 0, false
	}
	for n < len(s) && (s[n] >= 'a' && s[n] <= 'z' || s[n] >= 'A' && s[n] <= 'Z' || s[n] >= '0' && s[n] <= '9' || s[n] == '_' || s[n] == '-') {
		n++
	}
	return s[:n], n, n > 0
}

// tomlEscapes are the escapes of one character in a TOML basic string,
// with \e of TOML 1.1, as the TOML package reads them.
var tomlEscapes = map[byte]byte{'b': '\b', 't': '\t', 'n': '\n', 'f': '\f', 'r': '\r', 'e': 0x1b, '"': '"', '\\': '\\'}

// unescape reads the escapes of a TOML basic string, as the TOML package
// does: those of tomlEscapes, and \xHH, \uHHHH and \UHHHHHHHH. It keeps an
// escape that it does not know as it is: the file then has a syntax error,
// which is the finding.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		if c, ok := tomlEscapes[s[i+1]]; ok {
			b.WriteByte(c)
			i++
			continue
		}
		size := map[byte]int{'x': 2, 'u': 4, 'U': 8}[s[i+1]]
		if size > 0 && i+2+size <= len(s) {
			// 22 bits, with a sign, hold every rune. The TOML package refuses a
			// larger one: the file then has a syntax error.
			if r, err := strconv.ParseInt(s[i+2:i+2+size], 16, 22); err == nil {
				b.WriteRune(rune(r))
				i += 1 + size
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// blank skips spaces, line ends and comments.
func (l *lexer) blank() {
	for l.i < len(l.s) {
		switch l.s[l.i] {
		case ' ', '\t', '\r':
			l.i++
		case '\n':
			l.i++
			l.line++
		case '#':
			for l.i < len(l.s) && l.s[l.i] != '\n' {
				l.i++
			}
		default:
			return
		}
	}
}

// spaces skips spaces and tabs.
func (l *lexer) spaces() {
	for l.i < len(l.s) && (l.s[l.i] == ' ' || l.s[l.i] == '\t') {
		l.i++
	}
}

// endLine skips the rest of the line, such as a comment, and its end.
func (l *lexer) endLine() {
	for l.i < len(l.s) && l.s[l.i] != '\n' {
		l.i++
	}
	if l.i < len(l.s) {
		l.i++
		l.line++
	}
}
