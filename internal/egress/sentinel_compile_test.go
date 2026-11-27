package egress

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestSentinelGenericCompilerUsesFullClassesAndPublicSeparators(t *testing.T) {
	value := api.Secret("cfg_AAaa00-_./+=:!!")
	shape, err := compileSentinelFormat(context.Background(), "TOKEN", value, "cfg_", "", sentinelPunctuationPrintable)
	if err != nil {
		t.Fatal(err)
	}
	if shape.Prefix != "cfg_" || len(shape.Slots) != 11 || shape.Slots[0] != (sentinelSlot{sentinelUpper, 2}) || shape.Slots[1] != (sentinelSlot{sentinelLower, 2}) || shape.Slots[2] != (sentinelSlot{sentinelDigits, 2}) || shape.Slots[10] != (sentinelSlot{sentinelPrintablePunctuation, 2}) {
		t.Fatal("generic format used observed character sets instead of compact full classes")
	}
	for i, separator := range sentinelSeparators {
		if shape.Slots[i+3] != (sentinelSlot{string(separator), 1}) {
			t.Fatal("reviewed public separator structure changed")
		}
	}
	result, err := makeSentinelWithRead(context.Background(), "TOKEN", &value, shape, func(api.Secret) bool { return true }, scriptedSentinelRead(1, 1, 1, 1, 1, 1, 1, 1))
	if err != nil || len(result) != len(value) || !strings.Contains(string(result), sentinelSeparators) {
		t.Fatal("compiled generic sentinel changed its byte length or structural positions")
	}
}

func TestSentinelGenericPunctuationPolicyKeepsToken68PaddingStructural(t *testing.T) {
	value := api.Secret("pk_~==")
	shape, err := compileSentinelFormat(context.Background(), "TOKEN", value, "pk_", "", sentinelPunctuationToken68)
	if err != nil {
		t.Fatal(err)
	}
	if len(shape.Slots) != 2 || shape.Slots[0].Alphabet != sentinelToken68Punctuation || strings.Contains(shape.Slots[0].Alphabet, "=") || shape.Slots[1] != (sentinelSlot{"=", 2}) {
		t.Fatal("token68 compiler introduced variable padding or broader punctuation")
	}
	if _, err := compileSentinelFormat(context.Background(), "TOKEN", "pk_!", "pk_", "", sentinelPunctuationToken68); err == nil {
		t.Fatal("token68 punctuation policy accepted a non-token68 variable character")
	}
	if _, err := compileSentinelFormat(context.Background(), "TOKEN", value, "pk_", "", sentinelPunctuationUnset); err == nil {
		t.Fatal("compiler guessed an unstated punctuation policy")
	}
}

func TestSentinelGenericUnicodeUsesWholeReviewedSameWidthClasses(t *testing.T) {
	value := api.Secret("κ_αβ７")
	shape, err := compileSentinelFormat(context.Background(), "TOKEN", value, "κ_", "", sentinelPunctuationPrintable)
	if err != nil {
		t.Fatal(err)
	}
	if len(shape.Slots) != 2 || shape.Slots[0].Count != 2 || !strings.ContainsRune(shape.Slots[0].Alphabet, 'γ') || !strings.ContainsRune(shape.Slots[1].Alphabet, '８') {
		t.Fatal("Unicode compiler copied observed value codepoints rather than full classes")
	}
	for _, slot := range shape.Slots {
		first, _ := utf8.DecodeRuneInString(slot.Alphabet)
		width := utf8.RuneLen(first)
		for _, r := range slot.Alphabet {
			if utf8.RuneLen(r) != width {
				t.Fatal("Unicode alphabet mixed byte widths")
			}
		}
	}
	result, err := makeSentinelWithRead(context.Background(), "TOKEN", &value, shape, func(api.Secret) bool { return true }, scriptedSentinelRead(0, 0, 0, 0, 0, 0))
	if err != nil || len(result) != len(value) {
		t.Fatal("Unicode class generation changed byte length")
	}
	for _, r := range string(result)[len("κ_"):] {
		if !unicode.Is(unicode.Ll, r) && !unicode.Is(unicode.Nd, r) {
			t.Fatal("generated Unicode character left its reviewed class")
		}
	}
	for _, unsupported := range []api.Secret{"pk_字", "pk_🙂", "pk_e\u0301", "pk_\u200b", "pk_ "} {
		if _, err := compileSentinelFormat(context.Background(), "TOKEN", unsupported, "pk_", "", sentinelPunctuationPrintable); err == nil {
			t.Fatal("unsupported Unicode/whitespace domain silently broadened or copied a private literal")
		}
	}
}

func TestSentinelGenericShortValuesHaveNoMinimumLength(t *testing.T) {
	value := api.Secret("A")
	shape, err := compileSentinelFormat(context.Background(), "TOKEN", value, "", "", sentinelPunctuationPrintable)
	if err != nil || len(shape.Slots) != 1 || shape.Slots[0].Alphabet != sentinelUpper || shape.Slots[0].Count != 1 {
		t.Fatal("short format was rejected or reduced to its observed letter")
	}
	result, err := makeSentinelWithRead(context.Background(), "TOKEN", &value, shape, func(api.Secret) bool { return true }, scriptedSentinelRead(1))
	if err != nil || result != "B" {
		t.Fatal("short compiled format could not produce a distinct sentinel")
	}
	fixed, err := compileSentinelFormat(context.Background(), "TOKEN", "--", "", "", sentinelPunctuationPrintable)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := makeSentinelWithRead(context.Background(), "TOKEN", nil, fixed, func(api.Secret) bool { return true }, scriptedSentinelRead(0)); err == nil {
		t.Fatal("all-structural fixed format pretended to have a fresh alternative")
	}
}

func TestSentinelCompilerMismatchAndCancellationNeverEchoInputs(t *testing.T) {
	value := api.Secret("private-sensitive-format-value")
	_, err := compileSentinelFormat(context.Background(), "TOKEN", value, "different-prefix", "", sentinelPunctuationPrintable)
	var public *proto.Error
	if err == nil || !errors.As(err, &public) || public.Hint == "" || strings.Contains(err.Error(), string(value)) || strings.Contains(err.Error(), "different-prefix") {
		t.Fatal("format mismatch exposed inputs or lacked names-only diagnostics")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := compileSentinelFormat(ctx, "TOKEN", value, "", "", sentinelPunctuationPrintable); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled format compilation proceeded")
	}
}
