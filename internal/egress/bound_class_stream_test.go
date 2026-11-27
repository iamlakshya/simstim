package egress

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode"

	"github.com/iamlakshya/simstim/internal/api"
)

func fullPublicClassOracle(t *testing.T, slot api.SecretShapeSlot) (*sentinelPlan, error) {
	t.Helper()
	slot.Count = 1
	// This isolates the class component. It is not an accepted operational
	// rule ID and supplies no saved-state or pinned-path authority.
	public := &api.SecretShape{Version: api.SecretShapeV1, Rule: "class-component-fixture", Slots: []api.SecretShapeSlot{slot}}
	shape, err := legacyDecodePublicSecretShape(t.Context(), "KEY", public, sentinelPunctuationUnset)
	if err != nil {
		return nil, err
	}
	return compileSentinelShape(t.Context(), "KEY", shape)
}

func TestSymbolicPublicClassMatchesUnchangedFullCanonicalClassOracle(t *testing.T) {
	var cases []api.SecretShapeSlot
	for _, label := range savedClassLabels {
		for width := 0; width <= 5; width++ {
			cases = append(cases, api.SecretShapeSlot{Class: label, Width: width})
		}
		cases = append(cases, api.SecretShapeSlot{Class: label, Width: 1, ASCII: "A"})
	}
	for _, alphabet := range []string{"", "A", "AB", "BA", "AA", "-", " ", "!", "\t", "\x00", "\x7f", "é", "\xff", sentinelUpper, sentinelLower, sentinelDigits, sentinelPrintablePunctuation, sentinelToken68Punctuation} {
		cases = append(cases, api.SecretShapeSlot{Class: "ascii", Width: 1, ASCII: alphabet})
	}
	for _, label := range []string{"", "ASCII", "ascii-upper-more", "unicode-letter", "ascii\x00", "\xff"} {
		cases = append(cases, api.SecretShapeSlot{Class: label, Width: 1})
	}
	probes := []rune{-1, 0, '\t', '\n', ' ', '-', 'A', 'a', '0', 0x7f, 0x80, 0x7ff, 0x800, 0xffff, 0x10000, unicode.MaxRune}
	for _, table := range []*unicode.RangeTable{unicode.Lu, unicode.Ll, unicode.Nd} {
		for _, span := range table.R16 {
			probes = append(probes, rune(span.Lo)-1, rune(span.Lo), rune(span.Hi), rune(span.Hi)+1)
		}
		for _, span := range table.R32 {
			probes = append(probes, rune(span.Lo)-1, rune(span.Lo), rune(span.Hi), rune(span.Hi)+1)
		}
	}
	for _, slot := range cases {
		full, fullErr := fullPublicClassOracle(t, slot)
		symbolic, symbolicErr := symbolicPublicClass(t.Context(), "KEY", slot)
		if (fullErr == nil) != (symbolicErr == nil) {
			t.Fatal("symbolic class acceptance differs from the unchanged full canonical round trip and compilation")
		}
		if fullErr != nil {
			continue
		}
		if symbolic.variable != full.variable || symbolic.width != full.groups[0].width {
			t.Fatal("symbolic variability or byte width differs from the source class")
		}
		for _, r := range probes {
			_, want := full.groups[0].allowed[r]
			if symbolic.contains(r) != want {
				t.Fatal("symbolic class membership differs at a pinned Unicode/ASCII range boundary")
			}
		}
		for _, policy := range []sentinelPunctuationPolicy{sentinelPunctuationUnset, sentinelPunctuationPrintable, sentinelPunctuationToken68} {
			if symbolic.generic(policy) != genericPublicClass(slot, policy) {
				t.Fatal("symbolic generic policy changed the trusted source class")
			}
		}
	}
}

func TestSymbolicPublicClassRefusesPartialDuplicateAndCancelledState(t *testing.T) {
	state, ok := newSymbolicSavedClass(savedClassASCII, 1)
	if !ok || state.contains('A') || state.generic(sentinelPunctuationPrintable) {
		t.Fatal("partial class state exposed usable membership")
	}
	if !state.asciiRune('A') || state.asciiRune('A') || state.finish(t.Context(), "KEY") == nil || state.contains('A') {
		t.Fatal("duplicate input left a usable partial class")
	}
	state, _ = newSymbolicSavedClass(savedClassASCII, 1)
	if !state.asciiRune('-') || state.finish(t.Context(), "KEY") != nil || !state.contains('-') {
		t.Fatal("fresh value state did not reset a prior failure")
	}
	if state.asciiRune('A') || state.contains('-') || state.finish(t.Context(), "KEY") == nil {
		t.Fatal("finished class accepted late mutation")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	state, _ = newSymbolicSavedClass(savedClassUnicodeUpper, 2)
	if err := state.finish(ctx, "KEY"); !errors.Is(err, context.Canceled) || state.contains('Ж') {
		t.Fatal("cancelled class lost cancellation or exposed membership")
	}
	var label savedClassLabel
	label.reset()
	for _, r := range "ascii-upper-more" {
		if !label.rune(r) {
			break
		}
	}
	if label.finish() != savedClassInvalid {
		t.Fatal("invalid extended label became a known class")
	}
	label.reset()
	for _, r := range "ascii-upper" {
		if !label.rune(r) {
			t.Fatal("finite label reset retained a previous failure")
		}
	}
	if label.finish() != savedClassUpper {
		t.Fatal("finite label completion lost the exact class")
	}
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x"} {
		if fmt.Sprintf(format, label) != api.Redacted || fmt.Sprintf(format, state) != api.Redacted {
			t.Fatal("class validator formatting exposed partial metadata")
		}
	}
	// Duplicate detection is the existing semantic refusal, not a shorter
	// maximum alphabet. No large input is retained before that refusal.
	if _, err := symbolicPublicClass(t.Context(), "KEY", api.SecretShapeSlot{Class: "ascii", Width: 1, ASCII: strings.Repeat("A", 4096)}); err == nil {
		t.Fatal("repeated ASCII choices became a canonical class")
	}
}
