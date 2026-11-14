//go:build darwin || linux

package tomlinspect

import (
	"errors"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// Every former Span/%q/%s call remains a native lexical refusal at the
// same position. Inspector diagnostics intentionally expose no raw lexeme.
func TestBoundedLexicalDiagnosticProjectionKeepsNativePositions(t *testing.T) {
	factory := fixtureIndex(t)
	inspector, err := NewInspector(factory)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{
		"v=\"\\xG0\"\n", "v=\"\\u00G0\"\n", "v=\"\\U0000G000\"\n",
		"v=+ix\n", "v=-nx\n", "v=-0x1\n", "v=0b2\n", "v=0o8\n", "v=0xg\n",
	} {
		var decoded map[string]any
		_, wantErr := toml.Decode(input, &decoded)
		var want toml.ParseError
		if !errors.As(wantErr, &want) {
			t.Fatal("lexical diagnostic corpus lost its native refusal")
		}
		gotErr := inspector.Inspect(t.Context(), strings.NewReader(input), int64(len(input)), syntaxOnlyVisitor{})
		var got *Error
		if !errors.As(gotErr, &got) {
			t.Fatal("missing bounded native lexical refusal", gotErr)
		}
		if got.Position.Line != want.Position.Line || got.Position.Start != want.Position.Start || got.Position.Len != want.Position.Len || got.Position.Col != want.Position.Col {
			t.Fatalf("lexical source position changed: %+v want %+v", got.Position, want.Position)
		}
		if got.Cause == nil || !strings.Contains(got.Cause.Error(), "<redacted TOML token>") {
			t.Fatal("diagnostic lost its explicit bounded/redacted token projection")
		}
		if strings.Contains(got.Error(), "<redacted TOML token>") {
			t.Fatal("outer Inspector error contract unexpectedly changed")
		}
	}
}
