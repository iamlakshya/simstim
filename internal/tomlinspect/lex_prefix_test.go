//go:build darwin || linux

package tomlinspect

import (
	"errors"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestPackedLexerKeepsNativeUnbalancedPrefixErrors(t *testing.T) {
	factory := fixtureIndex(t)
	for _, control := range []bool{false, true} {
		input := []byte("a=" + strings.Repeat("[", MaxInput-2))
		position := len(input) - 1
		if control {
			input[3*MaxInput/4] = 0
			position = 3*MaxInput/4 - 1
		}
		s, err := newSource(t.Context(), strings.NewReader(string(input)), int64(len(input)))
		if err != nil {
			t.Fatal(err)
		}
		var stack lexStack
		if err := stack.reset(factory.workspace[:]); err != nil {
			t.Fatal(err)
		}
		lexer := lex(s, &stack)
		var failure item
		for {
			token := lexer.nextItem()
			if token.typ == itemError {
				failure = token
				break
			}
			if token.typ == itemEOF {
				t.Fatal("unbalanced input was accepted")
			}
		}
		if failure.pos.Start != position || failure.pos.Line != 1 || failure.pos.Len != 1 {
			t.Fatalf("native error position changed: %+v want byte %d", failure.pos, position)
		}
		if stack.maximum <= MaxInput/2 {
			t.Fatal("invalid-prefix test did not exceed the old valid-only stack bound")
		}
		if control {
			var character errLexControl
			if !errors.As(failure.err, &character) {
				t.Fatal("a later control error was replaced by an early depth refusal")
			}
		}
	}
}

func TestMetadataParseErrorPositionsMatchPinnedDecoder(t *testing.T) {
	factory := fixtureIndex(t)
	inspector, err := NewInspector(factory)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{
		"a=1\na=2\n", "a=1\n[a]\n", "a.b=1\na.b=2\n", "a={x=1,x=2}\n",
		"a={x.y=1,x=2,x.z=3}\n", "a={\"\"=[]}\na.\"\".x=1\n",
		"v=9223372036854775808\n", "v=1.e2\n", "\"\\uD800\"=1\n", "v=\"\\uD800\"\n",
		"\xef\xbb\xbfa=1\na=2\n",
	} {
		var decoded map[string]any
		_, oracleErr := toml.Decode(input, &decoded)
		var oracle toml.ParseError
		if !errors.As(oracleErr, &oracle) {
			t.Fatal("position corpus lost its pinned parse failure")
		}
		actualErr := inspector.Inspect(t.Context(), strings.NewReader(input), int64(len(input)), syntaxOnlyVisitor{})
		var actual *Error
		if !errors.As(actualErr, &actual) {
			t.Fatalf("missing native-position refusal: %v", actualErr)
		}
		want := oracle.Position
		if actual.Position.Line != want.Line || actual.Position.Start != want.Start || actual.Position.Len != want.Len || actual.Position.Col != want.Col {
			t.Fatalf("native source position differs: got %+v want %+v", actual.Position, want)
		}
	}
}
