package client

import (
	"strings"
	"testing"
	"time"
)

func TestSentDetailsKeepsIdentityAndRemovalBoundaries(t *testing.T) {
	h := Host{Name: "box"}
	id := strings.Repeat("a", 32)
	otherID := strings.Repeat("b", 32)
	at := time.Date(2026, 11, 9, 14, 2, 0, 0, time.UTC)
	row := func(host, hostID string, kind OutboundKind, name string, count int) GrantRecord {
		return GrantRecord{HostName: host, HostID: hostID, Kind: kind, Name: name, Count: count, FirstAt: at, LastAt: at}
	}
	removed := row(h.Name, id, OutboundSecret, "REMOVED_KEY", 1)
	removed.RemovedAt = &at
	records := []GrantRecord{
		row(h.Name, id, OutboundSecret, "Z_KEY", 2), row(h.Name, id, OutboundSecret, "A_KEY", 1),
		row("other", id, OutboundSecret, "WRONG_HOST", 1), row(h.Name, otherID, OutboundSecret, "WRONG_ID", 1),
		row(h.Name, id, OutboundSecret, "BAD_COUNT", -1), removed,
		row(h.Name, id, OutboundSSHAgent, "", 1), row(h.Name, id, OutboundPaste, "", 3),
	}
	rows := SentDetails(h, id, records)
	if len(rows) != 5 {
		t.Fatalf("got %d rows, want all five grant kinds", len(rows))
	}
	var lines []string
	for _, r := range rows {
		lines = append(lines, r.Label+" "+r.Text())
	}
	text := strings.Join(lines, "\n")
	for _, forbidden := range []string{"REMOVED_KEY", "WRONG_HOST", "WRONG_ID", "BAD_COUNT"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("display crossed a grant boundary for %s:\n%s", forbidden, text)
		}
	}
	if strings.Index(text, "A_KEY") > strings.Index(text, "Z_KEY") || !strings.Contains(text, "A_KEY  sent 1 time,") || !strings.Contains(text, "Z_KEY  sent 2 times,") {
		t.Fatalf("names/counts are not clear and sorted:\n%s", text)
	}
	for _, want := range []string{"ssh agent sent 1 time,", "ssh keys never", "repos never", "pastes 3 images"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q:\n%s", want, text)
		}
	}
}

func TestSentHeaderHandlesUnknownIdentity(t *testing.T) {
	h := Host{Name: "box"}
	for _, id := range []string{"", "short", strings.Repeat("z", 32)} {
		if got := SentHeader(h, TierShared, id); got != "Sent to box (shared, host ID unknown)" {
			t.Errorf("header for %q = %q", id, got)
		}
	}
	if got := SentHeader(h, TierOwn, strings.Repeat("a", 32)); got != "Sent to box (own, host ID aaaa…aaaa)" {
		t.Fatal(got)
	}
}

func TestRotateReminderHasNoEmptyListAndSortsNames(t *testing.T) {
	h := Host{Name: "box"}
	if got := RotateReminder(h, nil); got != "" {
		t.Fatalf("empty reminder = %q", got)
	}
	if got := RotateReminder(h, []string{"Z_KEY", "A_KEY"}); got != "Rotate these secrets, which box got: A_KEY, Z_KEY." {
		t.Fatal(got)
	}
}
