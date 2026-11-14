package cli

import (
	"strings"
	"testing"
)

// path builds a key path from keys, where "host#1" is element 1 of the
// array of tables host.
func path(keys ...string) keyPath {
	var p keyPath
	for _, k := range keys {
		name, at := k, -1
		if i := len(k) - 2; i > 0 && k[i] == '#' {
			name, at = k[:i], int(k[i+1]-'0')
		}
		p = p.add(name)
		if at >= 0 {
			p = p.index(at)
		}
	}
	return p
}

type lineCase struct {
	p    keyPath
	want int
}

func checkLines(t *testing.T, data string, cases []lineCase) {
	t.Helper()
	lines := scanTOML(data)
	for _, c := range cases {
		if got := lines.find(c.p); got != c.want {
			t.Errorf("the line of %s is %d, want %d", c.p.shown(), got, c.want)
		}
	}
}

// The line of each table, element and key (docs/contributing/cli-spec.md
// 13.3).
func TestKeyLines(t *testing.T) {
	checkLines(t, `# A comment [not] a header
top = 1
note = """
[ui]
motion = "in a string"
"""
ui.motion = "dotted"

[attach]
  detach_key = "ctrl+]"   # a comment
'scrollback' = "keep"

[keymap]
"session: delete" = "x"
"say \"hi\"" = "h"

[[host]]
name = "a"
machine = { provider = "lima" }

[[host]]
name = "b"
labels.zone = "eu"

[host.machine]
provider = "docker"

[ host . "labels" ]
x = [
  ["not", "a header"],
]
a . b.c=1
`, []lineCase{
		{path("top"), 2},
		{path("note"), 3},
		{path("ui", "motion"), 7},
		{path("ui"), 7},
		{path("attach"), 9},
		{path("attach", "detach_key"), 10},
		{path("attach", "scrollback"), 11},
		{path("keymap", "session: delete"), 14},
		{path("keymap", `say "hi"`), 15},
		{path("host"), 17},
		{path("host#0"), 17},
		{path("host#0", "name"), 18},
		{path("host#0", "machine"), 19},
		{path("host#0", "machine", "provider"), 19},
		{path("host#1", "name"), 22},
		{path("host#1", "labels"), 23},
		{path("host#1", "labels", "zone"), 23},
		{path("host#1", "machine"), 25},
		{path("host#1", "machine", "provider"), 26},
		{path("host#1", "labels", "x"), 29},
		{path("host#1", "labels", "a", "b", "c"), 32},
		{path("attach", "image_paste_key"), 0},
		{path("nowhere"), 0},
	})
}

// Quotes start a string only where a string can start: not in a comment,
// and not in another string (the reviews' cases).
func TestKeyLinesInStrings(t *testing.T) {
	checkLines(t, `[secrets]
A = 'say """'
B = """
[ui]
motoin = "x"
"""
[ui]
motoin = "y"
`, []lineCase{{path("secrets", "B"), 3}, {path("ui"), 7}, {path("ui", "motoin"), 8}})
	checkLines(t, `[attach]
scrollback = "keep" # was """clear"""? no: """
[ui]
motoin = "x"
`, []lineCase{{path("ui", "motoin"), 4}})
	checkLines(t, "[ui]\nmotion = \"on\" # \"\"\"\nmotoin = \"off\"\n", []lineCase{{path("ui", "motoin"), 3}})
	checkLines(t, `a = "x \"\"\" y"
b = """ one \""" two
three"""
c = '''lit
''''
d = 1
e = """\
  """
f = 2
`, []lineCase{{path("b"), 2}, {path("c"), 4}, {path("d"), 6}, {path("e"), 7}, {path("f"), 9}})
}

// An inline table may span lines, as TOML 1.1 allows, and its keys get
// their own lines. Each inline table in an array is an element with its
// own line.
func TestKeyLinesInInlineTables(t *testing.T) {
	checkLines(t, `[[host]]
name = "a"
machine = {
  provider = "lima",
  # a comment, with a "
  id = "a",
  user = "me",
}
user = "me"
`, []lineCase{{path("host#0", "machine"), 3}, {path("host#0", "machine", "user"), 7}, {path("host#0", "user"), 9}})
	checkLines(t, `host = [
  { name = "a", ssh = "a" },
  { name = "b", ssh = "b",
    shh = 1 },
  { name = "c, }", shh = 2, labels = { zone = 'eu, }' } },
]
`, []lineCase{
		{path("host"), 1}, {path("host#0"), 2}, {path("host#1"), 3}, {path("host#1", "shh"), 4},
		{path("host#2"), 5}, {path("host#2", "shh"), 5}, {path("host#2", "labels", "zone"), 5},
	})
}

// A key with more than maxKeyParts parts, or a value nested more than
// maxNesting levels, is recorded as too deep, and ends the scan before it
// records its parts.
func TestKeyLinesTooDeep(t *testing.T) {
	for _, data := range []string{
		"[" + strings.Repeat("a.", maxKeyParts-1) + "b]\nx = 1\n",
		"x = " + strings.Repeat("[", maxNesting) + strings.Repeat("]", maxNesting) + "\n",
		"x = " + strings.Repeat("{a=", maxNesting) + "1" + strings.Repeat("}", maxNesting) + "\n",
	} {
		if l := scanTOML(data); l.deep != 0 {
			t.Errorf("%.40q… is too deep", data)
		}
	}
	for _, data := range []string{
		"x = 1\n[" + strings.Repeat("a.", maxKeyParts) + "b]\n",
		"x = 1\n" + strings.Repeat("a.", maxKeyParts) + "b = 1\n",
		"x = 1\ny = { " + strings.Repeat("a.", maxKeyParts) + "b = 1 }\n",
		"x = 1\ny = " + strings.Repeat("[", maxNesting+1) + strings.Repeat("]", maxNesting+1) + "\nz = 1\n",
		"x = 1\ny = " + strings.Repeat("{a=", maxNesting+1) + "1" + strings.Repeat("}", maxNesting+1) + "\nz = 1\n",
		"x = 1\ny = [" + strings.Repeat("{a=[", maxNesting) + "\nz = 1\n",
	} {
		if l := scanTOML(data + "z = 1\n"); l.deep != 2 || len(l.lines) > 3+2*maxNesting || l.find(path("z")) != 0 {
			t.Errorf("%.40q… gives line %d and %d nodes, and goes on to the next key", data, l.deep, len(l.lines))
		}
	}
	// The full names of keys under a long table add up: the 20th key here
	// takes the total past maxKeyNames.
	long := "[" + strings.Repeat("a", 400000) + "]\n" + strings.Repeat("k = 1\n", 30)
	if l := scanTOML(long); l.deep != 21 || !strings.Contains(l.why, "8 MiB") {
		t.Errorf("a long table name with 30 keys gives line %d: %s", l.deep, l.why)
	}
}

// A byte order mark comes before the first line, as the TOML package
// skips it.
func TestKeyLinesAfterAByteOrderMark(t *testing.T) {
	for _, bom := range []string{"\xef\xbb\xbf", "\xff\xfe", "\xfe\xff"} {
		checkLines(t, bom+"[ui]\nmotoin = \"off\"\n", []lineCase{{path("ui"), 1}, {path("ui", "motoin"), 2}})
	}
}

// A quoted key is read with the escapes of TOML, as the TOML package reads
// it.
func TestKeyLinesOfEscapedKeys(t *testing.T) {
	checkLines(t, `[ui]
"mo\etoin" = "off"
"mo\u0074ion" = "on"
"\U0001F600\x41\b\t\n\f\r\"\\" = 1
"mo\qtion" = 2
"\U0010FFFD" = 3
`, []lineCase{
		{path("ui", "mo\x1btoin"), 2},
		{path("ui", "motion"), 3},
		{path("ui", "\U0001F600A\b\t\n\f\r\"\\"), 4},
		{path("ui", `mo\qtion`), 5},
		{path("ui", "\U0010FFFD"), 6},
	})
}

// The lexer never stops early or loops on a file with mistakes.
func TestKeyLinesInBrokenFiles(t *testing.T) {
	for _, data := range []string{"", "=", "[", "[[", "[a", "a =", "a = [", "a = {", "a = {,", "a = [1,,2]", "a = {b = ]}", `a = "`, "a = '''", `"a`, "'a", "a.", "a = { b = [ { c"} {
		_ = scanTOML(data)
		_ = scanTOML(data + "\nb = 1\n")
	}
	if l := scanTOML("a = [1,,2]\nb = 1\n"); l.find(path("b")) != 2 {
		t.Errorf("a broken line hides the next one")
	}
}
