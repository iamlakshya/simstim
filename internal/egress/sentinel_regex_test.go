package egress

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

// These small representative format grammars exercise fixed public markers
// and finite widths. They are not another operational minted-token rule set.
func TestSentinelReviewedGrammarPreservesTheFixedAASuffix(t *testing.T) {
	pattern := `\b(sk-ant-api03-[A-Za-z0-9_-]{3}AA)(?:[';\s]|$)`
	value := api.Secret("sk-ant-api03-abcAA")
	shape, err := compileSentinelFormat(context.Background(), "TOKEN", value, "sk-ant-api03-", pattern, sentinelPunctuationUnset)
	if err != nil {
		t.Fatal(err)
	}
	result, err := makeSentinelWithRead(context.Background(), "TOKEN", &value, shape, func(api.Secret) bool { return true }, scriptedSentinelRead(0, 0, 0))
	if err != nil || !strings.HasSuffix(string(result), "AA") || len(result) != len(value) || !regexp.MustCompile(pattern).MatchString(string(result)) {
		t.Fatal("reviewed fixed AA grammar was broadened into a generic token")
	}
}

func TestSentinelReviewedGrammarFreezesSelectedAlternativesAndMarkerLengths(t *testing.T) {
	pattern := `\b(sk-(?:proj|svcacct|admin)-(?:[A-Za-z0-9_-]{4}|[A-Za-z0-9_-]{2})T3BlbkFJ(?:[A-Za-z0-9_-]{4}|[A-Za-z0-9_-]{2})\b|sk-[A-Za-z0-9]{2}T3BlbkFJ[A-Za-z0-9]{2})(?:[';]|$)`
	for _, tc := range []struct {
		prefix      string
		left, right int
	}{{"sk-proj-", 2, 4}, {"sk-svcacct-", 4, 2}, {"sk-admin-", 2, 2}, {"sk-", 2, 2}} {
		value := api.Secret(tc.prefix + strings.Repeat("a", tc.left) + "T3BlbkFJ" + strings.Repeat("b", tc.right))
		shape, err := compileSentinelFormat(context.Background(), "TOKEN", value, "", pattern, sentinelPunctuationUnset)
		if err != nil {
			t.Fatal(err)
		}
		result, err := makeSentinelWithRead(context.Background(), "TOKEN", &value, shape, func(api.Secret) bool { return true }, scriptedSentinelRead(0))
		if err != nil {
			t.Fatal(err)
		}
		parts := strings.Split(string(result), "T3BlbkFJ")
		if len(parts) != 2 || !strings.HasPrefix(parts[0], tc.prefix) || len(parts[0])-len(tc.prefix) != tc.left || len(parts[1]) != tc.right || len(result) != len(value) || !regexp.MustCompile(pattern).MatchString(string(result)) {
			t.Fatal("selected finite branch, marker, boundary or repeat width changed")
		}
		if tc.prefix != "sk-" && strings.HasSuffix(string(result), "-") {
			t.Fatal("word-boundary-sensitive final class generated a hyphen")
		}
	}
}

func TestSentinelReviewedBoundedRangeUsesActualMatchedLength(t *testing.T) {
	pattern := `(pk_[A-Z]{2,4}AA)`
	value := api.Secret("pk_ABCAA")
	shape, err := compileSentinelFormat(context.Background(), "TOKEN", value, "pk_", pattern, sentinelPunctuationUnset)
	if err != nil {
		t.Fatal(err)
	}
	result, err := makeSentinelWithRead(context.Background(), "TOKEN", &value, shape, func(api.Secret) bool { return true }, scriptedSentinelRead(1, 1, 1))
	if err != nil || len(result) != len(value) || !strings.HasSuffix(string(result), "AA") || !regexp.MustCompile(pattern).MatchString(string(result)) {
		t.Fatal("bounded repeat did not keep the actual value's finite length")
	}
}

func TestSentinelReviewedInternalWordBoundaryKeepsBothNeighbourClasses(t *testing.T) {
	pattern := `(pk_[A-Za-z_-]{1}\b[_-]{1})`
	value := api.Secret("pk_A-")
	shape, err := compileSentinelFormat(context.Background(), "TOKEN", value, "pk_", pattern, sentinelPunctuationUnset)
	if err != nil {
		t.Fatal(err)
	}
	result, err := makeSentinelWithRead(context.Background(), "TOKEN", &value, shape, func(api.Secret) bool { return true }, scriptedSentinelRead(1))
	if err != nil || !strings.HasSuffix(string(result), "-") || !regexp.MustCompile(pattern).MatchString(string(result)) {
		t.Fatal("internal word boundary was treated as an ignorable zero-width token")
	}
}

func TestSentinelReviewedShortNonwordClassWithoutBoundaryRemainsVariable(t *testing.T) {
	value := api.Secret("pk_-")
	shape, err := compileSentinelFormat(context.Background(), "TOKEN", value, "pk_", `(pk_[-_A])`, sentinelPunctuationUnset)
	if err != nil {
		t.Fatal(err)
	}
	result, err := makeSentinelWithRead(context.Background(), "TOKEN", &value, shape, func(api.Secret) bool { return true }, scriptedSentinelRead(1))
	if err != nil || result == value || !regexp.MustCompile(`(pk_[-_A])`).MatchString(string(result)) {
		t.Fatal("an invented edge wordness constraint made a short mutable domain fixed")
	}
}

func TestSentinelReviewedNoWordBoundaryPreservesItsActualNeighbours(t *testing.T) {
	pattern := `(pk_[A-Za-z_-]{1}\B[A-Za-z_-]{1})`
	value := api.Secret("pk_AB")
	shape, err := compileSentinelFormat(context.Background(), "TOKEN", value, "pk_", pattern, sentinelPunctuationUnset)
	if err != nil {
		t.Fatal(err)
	}
	result, err := makeSentinelWithRead(context.Background(), "TOKEN", &value, shape, func(api.Secret) bool { return true }, scriptedSentinelRead(0))
	if err != nil || !regexp.MustCompile(pattern).MatchString(string(result)) {
		t.Fatal("no-word-boundary constraint was dropped while compiling mutable positions")
	}
}

func TestSentinelReviewedMalformedOrUnboundedGrammarNeverFallsBack(t *testing.T) {
	for _, pattern := range []string{
		`pk_[A-Z]{2}`, `(pk_[A-Z]*)`, `(pk_[A-Z]+)`, `(pk_.{2})`, `(?i)(pk_[a-z]{2})`,
		`((pk_[A-Z]{2}))`, `(pk_[A-Z]{2,})`, `(?=pk_)(pk_AB)`, `(pk_[\p{L}]{2})`, `(`,
		`((?:[A-Z]{1000}){1000}){1000}`,
	} {
		if _, err := compileSentinelFormat(context.Background(), "TOKEN", "pk_AB", "pk_", pattern, sentinelPunctuationPrintable); err == nil {
			t.Fatal("unsupported reviewed grammar silently fell back or widened")
		}
	}
	if _, err := compileSentinelFormat(context.Background(), "TOKEN", "pk_12AA", "pk_", `(pk_[A-Z]{2}AA)`, sentinelPunctuationPrintable); err == nil {
		t.Fatal("reviewed grammar mismatch silently used generic classes")
	}
}
