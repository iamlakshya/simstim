package egress

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestSentinelLongAlternatingUnicodeKeepsIndependentRunCountsAndFullClasses(t *testing.T) {
	const runs = 512
	var input strings.Builder
	for i := range runs {
		letter := "Α"
		if i%2 != 0 {
			letter = "α"
		}
		input.WriteString(strings.Repeat(letter, 1+i%3))
	}
	real := api.Secret(input.String())
	shape, err := compileSentinelFormat(t.Context(), "CREDENTIAL", real, "", "", sentinelPunctuationPrintable)
	if err != nil || len(shape.Slots) != runs {
		t.Fatal("long valid Unicode credential did not retain its source runs")
	}
	value, err := makeSentinelWithRead(t.Context(), "CREDENTIAL", &real, shape, func(api.Secret) bool { return true }, scriptedSentinelRead(0))
	if err != nil || value == real || len(value) != len(real) || !utf8.ValidString(string(value)) {
		t.Fatal("class reuse changed real inequality, run counts or exact UTF8 width")
	}
	actual, source := []rune(string(value)), []rune(string(real))
	if len(actual) != len(source) {
		t.Fatal("class reuse changed the number of character positions")
	}
	for i, r := range actual {
		if utf8.RuneLen(r) != utf8.RuneLen(source[i]) || unicode.Is(unicode.Lu, r) != unicode.Is(unicode.Lu, source[i]) || unicode.Is(unicode.Ll, r) != unicode.Is(unicode.Ll, source[i]) {
			t.Fatal("class reuse mixed upper/lower alphabets or byte widths")
		}
	}
	// Count allocations, not elapsed time or a changed performance ceiling.
	// Fixed source classes must not clone a rune table for every run.
	allocations := testing.AllocsPerRun(1, func() {
		if _, err := compileSentinelShape(t.Context(), "CREDENTIAL", shape); err != nil {
			t.Fatal(err)
		}
	})
	if allocations >= float64(runs) {
		t.Fatal("full Unicode class allocation multiplied by repeated source runs")
	}
}
