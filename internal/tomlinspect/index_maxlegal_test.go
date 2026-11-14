//go:build darwin || linux

package tomlinspect

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// These are the FULL unchanged one-MiB cases, not passing resource evidence.
// Root must admit execution and run the same whole actor gates separately.
func TestOwnedMetadataMaximumLegalAndDenseInputs(t *testing.T) {
	factory := fixtureIndex(t)
	inspector, err := NewInspector(factory)
	if err != nil {
		t.Fatal(err)
	}
	depth := (MaxInput - len("v=0\n")) / 2
	inlineDepth := (MaxInput - len("a=0\n")) / 4
	cases := []struct{ name, input string }{
		{"array-depth", "v=" + strings.Repeat("[", depth) + "0" + strings.Repeat("]", depth) + "\n"},
		{"inline-depth", strings.Repeat("a={", inlineDepth) + "a=0" + strings.Repeat("}", inlineDepth) + "\n"},
		{"command", "run=\"" + strings.Repeat("x", MaxInput-len("run=\"\"\n")) + "\"\n"},
		{"numeric", "v=1." + strings.Repeat("0", MaxInput-len("v=1.\n")) + "\n"},
		{"date", "v=1979-05-27T07:32:00." + strings.Repeat("9", MaxInput-len("v=1979-05-27T07:32:00.Z\n")) + "Z\n"},
	}
	keyLength := MaxInput / 2
	elements := (MaxInput - keyLength - 5) / 3
	largeKey := "\"" + strings.Repeat("k", keyLength) + "\"=[{}" + strings.Repeat(",{}", elements-1) + "]\n"
	cases = append(cases, struct{ name, input string }{"giant-key-inline-elements", padLegalTOML(largeKey)})
	elements = (MaxInput - len("a=[]\n") + 1) / 3
	shortArray := "a=[{}" + strings.Repeat(",{}", elements-1) + "]\n"
	cases = append(cases, struct{ name, input string }{"repeated-short-key-appends", padLegalTOML(shortArray)})
	var dense strings.Builder
	for index := 0; ; index++ {
		line := fmt.Sprintf("field_%06d=0\n", index)
		if dense.Len()+len(line) > MaxInput {
			break
		}
		dense.WriteString(line)
	}
	cases = append(cases, struct{ name, input string }{"flat-unique-fields", padLegalTOML(dense.String())})
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if len(test.input) != MaxInput {
				t.Fatalf("required full-size corpus changed: %d", len(test.input))
			}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			if err := inspector.Inspect(ctx, strings.NewReader(test.input), int64(len(test.input)), syntaxOnlyVisitor{}); err != nil {
				t.Fatal(err)
			}
			stats := inspector.Stats()
			if stats.PeakPins > 16 || stats.Pages*4096 > uint64(256*len(test.input)+(128<<10)) {
				t.Fatal("derived cache/file envelope was exceeded")
			}
			if stats.TrieVisits > uint64(32*len(test.input)+512) {
				t.Fatal("exact name index exceeded its source-derived work charge")
			}
			if factory.opened != factory.closed {
				t.Fatal("maximum input retained its index lease")
			}
		})
	}
}

func padLegalTOML(input string) string {
	if len(input) > MaxInput {
		panic("maximum-legal fixture exceeds unchanged input bound")
	}
	left := MaxInput - len(input)
	if left == 0 {
		return input
	}
	if left == 1 {
		return input + "\n"
	}
	return input + "#" + strings.Repeat("x", left-2) + "\n"
}
