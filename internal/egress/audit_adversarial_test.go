package egress

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestAuditPathDecodesSecretsDespiteMalformedEscapes(t *testing.T) {
	const value = "sk-test-0000000000000"
	sc := NewScrubber(map[string]string{"KEY": value})
	for _, tc := range []struct{ name, raw, want string }{
		{"deep", "/hook/%252573k-test-0000000000000", "/hook/[KEY]"},
		{"invalid-first", "/%ZZ/%2573k-test-0000000000000", "/%ZZ/[KEY]"},
		{"invalid-last", "/%2573k-test-0000000000000/%ZZ", "/[KEY]/%ZZ"},
		{"invalid-decoded", "/%25ZZ/%2573k-test-0000000000000", "/%ZZ/[KEY]"},
		{"incomplete-last", "/%73k-test-0000000000000/%2", "/[KEY]/%2"},
		{"lower-hex", "/hook/%73k-test-0000000000000/%2f", "/hook/[KEY]//"},
		{"format-char", "/hook/sk-test-%e2%80%8b0000000000000", "/hook/[KEY]"},
		{"raw-format-char", "/hook/sk-test-\u200b0000000000000", "/hook/[KEY]"},
		{"control-char", "/hook/sk-test-%000000000000000", "/hook/[KEY]"},
		{"format-split-escape", "/hook/%\u200b73k-test-0000000000000", "/hook/[KEY]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sc.ScrubPath(tc.raw); got != tc.want {
				t.Fatalf("scrubbed path = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAuditPathDiscardsEveryAuthorityBeforeDecoding(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"HTTP://example.com/v1/chat?private=value#fragment", "/v1/chat"},
		{"HtTpS://user:password@example.com/v1/chat", "/v1/chat"},
		{"custom+scheme://user:password@example.com/v1/chat", "/v1/chat"},
		{"HTTP://user:password@example.com/%ZZ", "/%ZZ"},
		{"HTTP://user:password@example.com", "/"},
		{"//user:password@example.com/v1/chat", "/v1/chat"},
		{"%48TTP%3a%2f%2fuser:password@example.com/v1/chat", "[path omitted]"},
		{"%48TTP%3a%2f%2fuser:pass%2fword@example.com/v1/chat", "[path omitted]"},
		{"H\u200bTTP://user:password@example.com/v1/chat", "[path omitted]"},
		{"%48%e2%80%8bTTP%3a%2f%2fuser:pass%2fword@example.com/v1/chat", "[path omitted]"},
		{"/v1/name%20with%20spaces/caf%c3%a9", "/v1/name with spaces/café"},
		{"/v1%3fprivate_data=not_logged", "/v1"},
		{"/v1%23private_fragment", "/v1"},
		{"/v1/http://example.com/chat", "/v1/http://example.com/chat"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			if got := NewScrubber(nil).ScrubPath(tc.raw); got != tc.want {
				t.Fatalf("scrubbed URI = %q, want %q", got, tc.want)
			}
		})
	}
}

func FuzzAuditPathKeepsSecretsOut(f *testing.F) {
	for _, seed := range []string{"", "%ZZ", "%25ZZ/%2573", "%e2%80%8b", "HTTP://user:password@example.com/", strings.Repeat("%25", 40)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, prefix string) {
		sc := NewScrubber(map[string]string{"KEY": "sk-test-0000000000000"})
		got := sc.ScrubPath("/" + prefix + "/sk-test-%e2%80%8b0000000000000")
		if strings.Contains(got, "sk-test-0000000000000") || !utf8.ValidString(got) || len(got) > MaxAuditPathBytes {
			t.Fatalf("unsafe audit path %q", got)
		}
	})
}

func TestAuditPathBoundsCannotExposeAnEncodedSecretPrefix(t *testing.T) {
	sc := NewScrubber(map[string]string{"KEY": "sk-test-0000000000000"})
	for _, tail := range []string{"%", "%2", "%20"} {
		raw := "http://example.com/hook/%73k-test-0000000000000/"
		raw += strings.Repeat("a", maxScrubInputBytes-len(raw)-len(tail)) + tail + "/extra"
		if got := sc.ScrubPath(raw); got != "[path omitted]" {
			t.Fatalf("oversized path = %q, want a fixed omission marker", got)
		}
	}
	encoded := "%73k-test-0000000000000"
	for range 64 {
		encoded = strings.ReplaceAll(encoded, "%", "%25")
	}
	if got := sc.ScrubPath("/hook/" + encoded); got != "[path omitted]" {
		t.Fatalf("over-nested path = %q, want a fixed omission marker", got)
	}
	encoded = "/sk-test-0000000000000"
	for range 6 {
		var next strings.Builder
		for i := range len(encoded) {
			next.WriteByte('%')
			next.WriteString(hex.EncodeToString([]byte{encoded[i]}))
		}
		encoded = next.String()
	}
	if got := sc.ScrubPath(encoded); got != "[path omitted]" {
		t.Fatalf("an oversized encoded path exposed a credential prefix: %q", got)
	}
	// Scrub before the final byte limit, including when removing hidden runes
	// brings a whole secret back across that limit.
	raw := "/" + strings.Repeat("a", 248) + "/sk-test-\u200b0000000000000"
	got := sc.ScrubPath(raw)
	if strings.Contains(got, "sk-") || len(got) > MaxAuditPathBytes || !utf8.ValidString(got) {
		t.Fatalf("partial secret or invalid text at the final bound: %q", got)
	}
	longSecret := "sk-" + strings.Repeat("q", 600)
	longScrubber := NewScrubber(map[string]string{"KEY": longSecret})
	got = longScrubber.ScrubPath("/" + strings.Repeat("a", 200) + "/sk-\u200b" + strings.Repeat("q", 600))
	if strings.Contains(got, "sk-") || !strings.Contains(got, "[KEY]") {
		t.Fatalf("normalisation truncated the secret before redaction: %q", got)
	}
}

func TestAuditNormalisationCannotReconstructSecretFields(t *testing.T) {
	const value = "secretword"
	sc := NewScrubber(map[string]string{"KEY": value})
	e := FormatAudit(AuditEntry{Host: "secret\u200bword", Method: "secretword", Path: "/secret\u200bword"}, sc, time.Time{})
	if e.Host != "[KEY]" || e.Method != "[KEY]" || e.Path != "/[KEY]" {
		t.Fatalf("normalised secret fields leaked: %+v", e)
	}
}

func TestAuditPathRedactsBeforeRemovingDecodedComponents(t *testing.T) {
	for _, tc := range []struct{ secret, raw string }{
		{"ab?cd", "/ab\u200b%3fcd"},
		{"ab#cd", "/ab\u200b%23cd"},
		{"ab\u200bcd", "/ab%e2%80%8bcd"},
	} {
		sc := NewScrubber(map[string]string{"KEY": tc.secret})
		if got := sc.ScrubPath(tc.raw); got != "/[KEY]" {
			t.Fatalf("normalised secret %q from %q became %q, want /[KEY]", tc.secret, tc.raw, got)
		}
	}
}

func TestAuditPathBoundsRepeatedReplacementGrowth(t *testing.T) {
	// A generated label stays stable while a neighbouring escape decodes.
	// A genuinely oversized rendered result must still hit the hard bound.
	sc := NewScrubber(map[string]string{"KK": "K"})
	raw := "/K%" + strings.Repeat("25", 8) + "73"
	if got := sc.ScrubPath(raw); got != "/[KK]s" {
		t.Fatalf("a generated label changed during decoding: %q", got)
	}
	if got := sc.ScrubPath("/" + strings.Repeat("K", maxScrubInputBytes-1)); got != "[path omitted]" {
		t.Fatalf("oversized rendered labels escaped the intermediate bound: %q", got)
	}
	largeValue := "sk-" + strings.Repeat("q", maxScrubInputBytes)
	largeScrubber := NewScrubber(map[string]string{"KEY": largeValue})
	if got := largeScrubber.ScrubPath("/" + largeValue); got != "[path omitted]" {
		t.Fatalf("oversized exposed value left a partial diagnostic: %q", got)
	}
	if got := largeScrubber.ScrubPath("/ordinary/path"); got != "/ordinary/path" {
		t.Fatalf("an unused large secret removed an ordinary path: %q", got)
	}
	longName := strings.Repeat("K", maxScrubInputBytes)
	if got := NewScrubber(map[string]string{longName: "v"}).ScrubPath("/v"); got != "[path omitted]" {
		t.Fatalf("oversized replacement tag escaped the buffer: %q", got)
	}
}
