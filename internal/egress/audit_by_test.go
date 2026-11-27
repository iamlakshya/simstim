package egress

import (
	"strings"
	"testing"
	"time"
)

func TestAuditProvenanceIsFiniteAndFullReasonIsScrubbedBeforeItsCap(t *testing.T) {
	value := strings.Repeat("K", 256)
	scrubber := NewScrubber(map[string]string{"KEY": value})
	row := FormatAudit(AuditEntry{Decision: "allow", Rule: "forged:classifier", Reason: value + "after", By: "guest-chosen"}, scrubber, time.Now())
	if row.By != "" || strings.Contains(row.Reason, "KK") || row.Reason != "[KEY]after" {
		t.Fatal("audit inferred provenance or cut a known secret before redaction")
	}
	for _, by := range []string{"live", "classifier"} {
		row := FormatAudit(AuditEntry{Decision: "allow", Rule: "opaque", By: by}, scrubber, time.Now())
		if row.By != by {
			t.Fatal("finite actual source provenance was discarded")
		}
	}
}
