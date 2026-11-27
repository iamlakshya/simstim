package egress

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestAuditScrubsSecretsAndSanitizesPaths(t *testing.T) {
	secrets := map[string]string{
		"SHORT_KEY": "sk-prefix",
		"LONG_KEY":  "sk-prefix-full-secret-value",
		"SLASH_KEY": "tok/with+special=chars",
	}
	sc := NewScrubber(secrets)
	fixedNow := time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC)

	// Longest secret wins so a prefix secret never leaks the tail of a longer secret.
	e := FormatAudit(AuditEntry{
		Host:     "api.example.com",
		Port:     80,
		Mode:     "explicit",
		Method:   "GET",
		Path:     "http://api.example.com/v1/keys/sk-prefix-full-secret-value/check?query=sk-prefix&raw=never_logged#frag",
		Status:   200,
		Rule:     "session:g123456",
		Decision: "allow",
	}, sc, fixedNow)

	if e.Path != "/v1/keys/[LONG_KEY]/check" {
		t.Fatalf("scrubbed path = %q, want /v1/keys/[LONG_KEY]/check", e.Path)
	}
	for _, v := range secrets {
		if strings.Contains(e.Path, v) {
			t.Fatalf("secret value %q leaked in path %q", v, e.Path)
		}
	}
	if strings.Contains(e.Path, "query") || strings.Contains(e.Path, "never_logged") || strings.Contains(e.Path, "frag") {
		t.Fatalf("query or fragment leaked in path %q", e.Path)
	}

	// Single and double URL-encoded secrets in the path are also scrubbed.
	encOnce := "/auth/" + url.PathEscape(secrets["SLASH_KEY"]) + "/ok"
	if got := sc.ScrubPath(encOnce); got != "/auth/[SLASH_KEY]/ok" {
		t.Errorf("single-encoded secret path = %q, want /auth/[SLASH_KEY]/ok", got)
	}
	encTwice := "/auth/" + url.PathEscape(url.PathEscape(secrets["LONG_KEY"])) + "/ok"
	if got := sc.ScrubPath(encTwice); got != "/auth/[LONG_KEY]/ok" {
		t.Errorf("double-encoded secret path = %q, want /auth/[LONG_KEY]/ok", got)
	}

	// Secret straddling the 256-byte path limit is scrubbed before truncation
	// so no prefix of the secret value leaks at the cut boundary.
	pad := "/" + strings.Repeat("a", 248) + "/"
	straddle := pad + secrets["LONG_KEY"]
	gotStraddle := sc.ScrubPath(straddle)
	if len(gotStraddle) > MaxAuditPathBytes || !utf8.ValidString(gotStraddle) {
		t.Fatalf("straddle len = %d valid=%v", len(gotStraddle), utf8.ValidString(gotStraddle))
	}
	if strings.Contains(gotStraddle, "sk-") {
		t.Fatalf("partial secret leaked at 256-byte boundary: %q", gotStraddle)
	}

	// Control characters, newlines, and ANSI sequences cannot forge a second JSONL line.
	forged := "/ok\r\n{\"v\":1,\"kind\":\"net\",\"decision\":\"allow\"}\x1b[31m/end"
	clean := FormatAudit(AuditEntry{Host: "example.com\nforged", Port: 443, Method: "GET\r\n", Path: forged}, sc, fixedNow)
	rawJSON, err := json.Marshal(clean)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(clean.Path, "\r\n\x1b") || strings.ContainsAny(clean.Host, "\r\n") || strings.Contains(string(rawJSON), "\n") {
		t.Fatalf("control characters survived audit formatting: path=%q host=%q json=%s", clean.Path, clean.Host, rawJSON)
	}
}
