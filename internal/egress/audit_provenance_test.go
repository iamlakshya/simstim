package egress

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAuditRedactionPreservesTheMatchedSecretIdentity(t *testing.T) {
	for _, tc := range []struct {
		name    string
		secrets map[string]string
		raw     string
		want    string
	}{
		{"short name contains value", map[string]string{"KK": "K"}, "/K", "/[KK]"},
		{"short name beside escape", map[string]string{"KK": "K"}, "/K/%61", "/[KK]/a"},
		{"generated label", map[string]string{"A": "K", "B": "[A]"}, "/K", "/[A]"},
		{"raw label is a value", map[string]string{"A": "K", "B": "[A]"}, "/[A]", "/[B]"},
		{"encoded value", map[string]string{"A": "K", "B": "[A]"}, "/%4b", "/[A]"},
		{"encoded raw label", map[string]string{"A": "K", "B": "[A]"}, "/%5bA%5d", "/[B]"},
		{"cleaned raw label", map[string]string{"A": "K", "B": "[A]"}, "/[\u200bA]", "/[B]"},
		{"split escape", map[string]string{"A": "K", "B": "[A]"}, "/%\u200b4b", "/[A]"},
		{"mixed origins", map[string]string{"A": "K", "B": "[A]"}, "/K/[A]/%61", "/[A]/[B]/a"},
		{"encoded mixed origins", map[string]string{"A": "K", "B": "[A]"}, "/%4b/%5bA%5d", "/[A]/[B]"},
		{"encoded percent value", map[string]string{"A": "%41", "B": "A"}, "/%2541", "/[A]"},
		{"literal raw variant priority", map[string]string{"A": "%41", "B": "A"}, "/%41", "/[A]"},
		{"distinct literal value", map[string]string{"A": "%41", "B": "A"}, "/A", "/[B]"},
		{"overlapping values", map[string]string{"LONG": "token-value", "SHORT": "token"}, "/token-value/token", "/[LONG]/[SHORT]"},
		{"guest frame spelling", map[string]string{"A": "K"}, "/!0!/K", "/!0!/[A]"},
		{"encoded guest frame", map[string]string{"A": "K"}, "/%21%30%21/K", "/!0!/[A]"},
		{"guest encoded-byte spelling", map[string]string{"A": "K"}, "/x4b/K", "/x4b/[A]"},
		{"marker is a real value", map[string]string{"FRAME": "!0!"}, "/!0!", "/[FRAME]"},
		{"no half-byte match", map[string]string{"A": "A"}, "/\x14\x10", "/"},
		{"binary value before cleaning", map[string]string{"BINARY": "\xff"}, "/%ff", "/[BINARY]"},
		{"UTF8 value", map[string]string{"NAME": "café"}, "/caf%c3%a9", "/[NAME]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := NewScrubber(tc.secrets).ScrubPath(tc.raw); got != tc.want {
				t.Fatalf("redacted %q as %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestAuditFieldCleaningKeepsGeneratedAndRawLabelsDistinct(t *testing.T) {
	sc := NewScrubber(map[string]string{"A": "K", "B": "[A]"})
	for _, tc := range []struct{ value, path, want, wantPath string }{
		{"K", "/K", "[A]", "/[A]"},
		{"[A]", "/[A]", "[B]", "/[B]"},
		{"[\u200bA]", "/[\u200bA]", "[B]", "/[B]"},
	} {
		got := FormatAudit(AuditEntry{Host: tc.value, Method: tc.value, Path: tc.path}, sc, time.Time{})
		if got.Host != tc.want || got.Method != tc.want || got.Path != tc.wantPath {
			t.Fatalf("field cleaning changed the matched secret identity: %+v", got)
		}
	}
}

func TestAuditOpaqueTargetsAndLongDiscardedQueries(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"http:user:password@example.com/v1", omittedAuditPath},
		{"user:password@example.com:80/v1", omittedAuditPath},
		{"//user:password@example.com/v1", "/v1"},
		{"HTTP://user:password@example.com?x=1", "/"},
		{"relative/path", omittedAuditPath},
		{"*", "*"},
		{"", ""},
		{"/kept?" + strings.Repeat("q", 12<<10), "/kept"},
		{"/kept#" + strings.Repeat("q", 12<<10), "/kept"},
		{strings.Repeat("q", maxHTTPHeaderBytes+1) + "?discard", omittedAuditPath},
	} {
		t.Run(tc.want, func(t *testing.T) {
			if got := NewScrubber(nil).ScrubPath(tc.raw); got != tc.want {
				t.Fatalf("target became %q, want %q", got, tc.want)
			}
		})
	}
	// A configured short value cannot hide a source authority from the
	// independent syntax guard by replacing part of its scheme.
	if got := NewScrubber(map[string]string{"SCHEME": "H"}).ScrubPath("H\u200bTTP://user:password@example.com/v1"); got != omittedAuditPath {
		t.Fatalf("redaction concealed a reconstructed authority: %q", got)
	}
}

func TestFormatAuditUsesOneImmutableSecretSnapshot(t *testing.T) {
	sc := NewScrubber(map[string]string{"ALPHA": "K"})
	start := make(chan struct{})
	var done sync.WaitGroup
	done.Add(1)
	go func() {
		defer done.Done()
		<-start
		for range 200 {
			sc.SetSecrets(map[string]string{"ALPHA": "K"})
			sc.SetSecrets(map[string]string{"BETA": "K"})
		}
	}()
	close(start)
	for range 400 {
		got := FormatAudit(AuditEntry{Host: "K", Method: "K", Path: "/K"}, sc, time.Time{})
		if (got.Host != "[ALPHA]" && got.Host != "[BETA]") || got.Method != got.Host || got.Path != "/"+got.Host {
			t.Errorf("one audit event mixed secret registries: %+v", got)
		}
	}
	done.Wait()
}

func FuzzAuditGeneratedLabelsKeepTheirProvenance(f *testing.F) {
	for _, tail := range []string{"", "[A]", "%41", "%5bA%5d", "!0!", "x4b", "%\u200b4b", strings.Repeat("%25", 40)} {
		f.Add(tail)
	}
	f.Fuzz(func(t *testing.T, tail string) {
		sc := NewScrubber(map[string]string{"A": "K", "B": "[A]"})
		got := sc.ScrubPath("/K/" + tail)
		if got != omittedAuditPath && !strings.HasPrefix(got, "/[A]/") {
			t.Fatalf("later guest text changed an already matched label: %q", got)
		}
		if len(got) > MaxAuditPathBytes {
			t.Fatalf("provenance handling exceeded the display bound: %d", len(got))
		}
	})
}
