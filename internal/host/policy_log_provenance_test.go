package host

import (
	"github.com/iamlakshya/simstim/internal/api"
	"testing"
	"time"
)

func TestAuditFilterUsesFiniteActualProvenanceAndKeepsTimeAndLegacyRules(t *testing.T) {
	entry := api.NetAuditEntry{Kind: "net", Decision: "allow", Rule: "g234567", By: "classifier", T: "2026-01-02T03:04:05Z"}
	if !auditEntryMatches(entry, time.Time{}, "classifier") {
		t.Fatal("actual classifier provenance omitted")
	}
	entry.By = "live"
	if auditEntryMatches(entry, time.Time{}, "classifier") {
		t.Fatal("different actual provenance selected")
	}
	entry.By = "classifier"
	if auditEntryMatches(entry, time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC), "classifier") {
		t.Fatal("provenance bypassed the original time filter")
	}
	entry.By = ""
	entry.Rule = "classifier:legacy"
	if !auditEntryMatches(entry, time.Time{}, "classifier") {
		t.Fatal("legacy filter semantics changed")
	}
}
