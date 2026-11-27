package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/paths"
)

func eventAuditFixture(t *testing.T, value alerts.Event, kind string) string {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := os.MkdirAll(paths.SessionDir("abcd2345"), 0o700); err != nil {
		t.Fatal(err)
	}
	record := map[string]any{"time": time.Now().UTC(), "event": kind, "fields": map[string]any{"value": value}}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	path := paths.AuditLog("abcd2345")
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func replayModelEvent() alerts.Event {
	return alerts.Event{V: 1, Event: alerts.SandboxError, Session: alerts.Session{ID: "abcd2345"}, Time: "2026-11-15T10:00:00Z", Stream: "0123456789abcdef0123456789abcdef", Sequence: 1}
}

// These are format/native-read models. The separate supervisor test invokes
// the actual original-audit publisher; no fixture is creation/lineage proof.
func TestEventReplayReadsOnlyExactPreparedNativeRecord(t *testing.T) {
	eventAuditFixture(t, replayModelEvent(), "session_event")
	ctx, cancel := context.WithTimeout(t.Context(), auditPageReadBudget)
	defer cancel()
	events, err := readEventAudit(ctx, "abcd2345", "abcd2345")
	if err != nil || len(events) != 1 || events[0].Event != alerts.SandboxError {
		t.Fatalf("events=%+v err=%v", events, err)
	}
}

func TestEventReplayDoesNotTurnRawGuestOrLifeDataIntoEvents(t *testing.T) {
	eventAuditFixture(t, replayModelEvent(), "guest_state")
	ctx, cancel := context.WithTimeout(t.Context(), auditPageReadBudget)
	defer cancel()
	events, err := readEventAudit(ctx, "abcd2345", "")
	if err != nil || len(events) != 0 {
		t.Fatalf("raw metadata became an event: %+v %v", events, err)
	}
}

func TestEventReplayRejectsPartialAndSymlinkedAudit(t *testing.T) {
	path := eventAuditFixture(t, replayModelEvent(), "session_event")
	if err := os.WriteFile(path, []byte(`{"event":"session_event"`), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), auditPageReadBudget)
	defer cancel()
	if _, err := readEventAudit(ctx, "abcd2345", ""); err == nil {
		t.Fatal("an incomplete native audit became a complete history")
	}
	other := filepath.Join(t.TempDir(), "audit.jsonl")
	if err := os.Rename(path, other); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, path); err != nil {
		t.Fatal(err)
	}
	if _, err := readEventAudit(ctx, "abcd2345", ""); err == nil {
		t.Fatal("a linked audit supplied native event records")
	}
}
