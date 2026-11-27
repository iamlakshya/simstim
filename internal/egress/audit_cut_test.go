package egress

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestAuditQueryCutSurvivesSecretReplacement(t *testing.T) {
	for _, tc := range []struct {
		name    string
		secrets map[string]string
		raw     string
		want    string
	}{
		{"lower query", map[string]string{"S": "ab?cd"}, "/ab%3fcd&private=value", "/[S]"},
		{"upper variant", map[string]string{"S": "ab?cd"}, "/ab%3Fcd&private=value", "/[S]"},
		{"deep query", map[string]string{"S": "ab?cd"}, "/ab%25253fcd&private=value", "/[S]"},
		{"lower fragment", map[string]string{"S": "ab#cd"}, "/ab%23cd&private=value", "/[S]"},
		{"deep fragment", map[string]string{"S": "ab#cd"}, "/ab%252523cd&private=value", "/[S]"},
		{"query at match start", map[string]string{"S": "?cd"}, "/%3fcd&private=value", "/"},
		{"upper query at match start", map[string]string{"S": "?cd"}, "/%3Fcd&private=value", "/"},
		{"fragment at match start", map[string]string{"S": "#cd"}, "/%23cd&private=value", "/"},
		{"literal variant starts at cut", map[string]string{"S": "%3f"}, "/ab%3f&private=value", "/ab"},
		{"tag ends at cut", map[string]string{"S": "ab"}, "/ab%3fprivate=value", "/[S]"},
		{"tag after cut", map[string]string{"S": "private=value"}, "/path%3fprivate=value&more=data", "/path"},
		{"two tags cross cut", map[string]string{"FIRST": "x", "S": "ab?cd"}, "/x/ab%3fcd&private=value", "/[FIRST]/[S]"},
		{"hidden source rune", map[string]string{"S": "ab?cd"}, "/ab\u200b%3fcd&private=value", "/[S]"},
		{"hidden escape rune", map[string]string{"S": "ab?cd"}, "/ab%\u200b3fcd&private=value", "/[S]"},
		{"decoded control", map[string]string{"S": "ab?cd"}, "/ab%003f%3fcd&private=value", "/ab3f"},
		{"control reconstructs secret", map[string]string{"S": "ab?cd"}, "/ab%00%3fcd&private=value", "/[S]"},
		{"secret includes hidden rune", map[string]string{"S": "ab\u200b?cd"}, "/ab%e2%80%8b%3fcd&private=value", "/[S]"},
		{"binary secret", map[string]string{"S": "\xff?cd"}, "/%ff%3fcd&private=value", "/[S]"},
		{"invalid source byte", nil, "/\xff%3fprivate=value", "/\ufffd"},
		{"cleaning before cut", nil, "/\u200bkept%3fprivate=value", "/kept"},
		{"unmatched invalid escape", nil, "/%ZZ/kept%253fprivate=value", "/%ZZ/kept"},
		{"no secret query", nil, "/kept%3fprivate=value", "/kept"},
		{"no secret deep fragment", nil, "/kept%2523private=value", "/kept"},
		{"ordinary useful path", nil, "/kept/caf%c3%a9", "/kept/café"},
		{"raw query projection", map[string]string{"S": "ab?cd"}, "/ab?cd&private=value", "/ab"},
		{"raw long query", nil, "/kept?" + strings.Repeat("q", 12<<10), "/kept"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := NewScrubber(tc.secrets).ScrubPath(tc.raw)
			if got != tc.want || len(got) > MaxAuditPathBytes || !utf8.ValidString(got) {
				t.Fatalf("audit path %q became %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestAuditQueryCutKeepsRawAndGeneratedIdentities(t *testing.T) {
	for _, tc := range []struct {
		name    string
		secrets map[string]string
		raw     string
		want    string
	}{
		{"generated and raw labels", map[string]string{"A": "K", "B": "[A]", "S": "ab?cd"}, "/K/[A]/ab%3fcd&private=value", "/[A]/[B]/[S]"},
		{"encoded raw label", map[string]string{"A": "K", "B": "[A]", "S": "ab?cd"}, "/K/%5bA%5d/ab%3fcd&private=value", "/[A]/[B]/[S]"},
		{"cleaned raw label", map[string]string{"A": "K", "B": "[A]", "S": "ab?cd"}, "/K/[\u200bA]/ab%\u200b3fcd&private=value", "/[A]/[B]/[S]"},
		{"raw label ends at cut", map[string]string{"A": "K", "B": "[A]"}, "/K/[A]%3fprivate=value", "/[A]/[B]"},
		{"specific literal variant", map[string]string{"S": "ab?cd", "LITERAL": "ab%3Fcd"}, "/ab%3Fcd&private=value", "/[LITERAL]"},
		{"specific double variant", map[string]string{"S": "ab?cd", "LITERAL": "ab%3Fcd"}, "/ab%253Fcd&private=value", "/[LITERAL]"},
		{"encoded percent identity", map[string]string{"A": "%41", "B": "A"}, "/%2541%3fprivate=value", "/[A]"},
		{"generated delimiter is not source", map[string]string{"A?Q": "K"}, "/K/ok", "/[A?Q]/ok"},
		{"query across protected span", map[string]string{"DIGIT": "3"}, "/ab%\u200b3fcd&private=value", "/ab"},
		{"fragment across protected span", map[string]string{"DIGIT": "2"}, "/ab%\u200b23cd&private=value", "/ab"},
		{"private frame spelling", map[string]string{"A": "K"}, "/!0:4!/K%3fprivate=value", "/!0:4!/[A]"},
		{"byte alignment before cut", map[string]string{"A": "A"}, "/\x14\x10%3fprivate=value", "/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := NewScrubber(tc.secrets).ScrubPath(tc.raw); got != tc.want {
				t.Fatalf("query clipping changed secret provenance: %q became %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestAuditQueryCutRedactsBeforeFinalPathBound(t *testing.T) {
	value := strings.Repeat("s", 400) + "?tail"
	prefix := "/" + strings.Repeat("p", 200)
	raw := prefix + strings.Repeat("s", 400) + "%3ftail&private=value"
	if got := NewScrubber(map[string]string{"S": value}).ScrubPath(raw); got != prefix+"[S]" {
		t.Fatalf("query cut exposed a partial secret at the final display bound: %q", got)
	}
	// The new cutoff metadata does not relax any source or display bound.
	for _, raw := range []string{
		"/" + strings.Repeat("p", maxScrubInputBytes) + "%3fprivate=value",
		"/kept%" + strings.Repeat("25", maxScrubDecodePasses+1) + "3fprivate=value",
	} {
		if got := NewScrubber(nil).ScrubPath(raw); got != omittedAuditPath {
			t.Fatalf("query-cut tracking bypassed an existing omission bound: %q", got)
		}
	}
}
