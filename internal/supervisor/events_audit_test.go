package supervisor

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
)

func TestEventPublisherPersistsPreparedV1ThroughOriginalAuditResource(t *testing.T) {
	root := t.TempDir()
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	path := filepath.Join(root, "audit.jsonl")
	audit := openAudit(path, slog.New(slog.DiscardHandler))
	defer audit.close()
	dispatcher := alerts.New(alerts.Options{Path: filepath.Join(root, "missing-alerts.toml"), PublishEvent: audit.writeEvent})
	defer dispatcher.Close(time.Second)
	dispatcher.Notify(alerts.Event{Event: alerts.SandboxError, Session: alerts.Session{ID: "abcd2345", Name: "job"}, Detail: "private question", DetailFrom: alerts.FromGuest})
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		Event  string `json:"event"`
		Fields struct {
			Value alerts.Event `json:"value"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(raw, &record); err != nil || record.Event != "session_event" || record.Fields.Value.V != 1 || record.Fields.Value.Event != alerts.SandboxError || record.Fields.Value.Detail != "" || record.Fields.Value.DetailFrom != "" {
		t.Fatalf("native audit record=%s err=%v", raw, err)
	}
	if _, err := alerts.StreamEventIdentity(record.Fields.Value); err != nil {
		t.Fatal(err)
	}
}

func TestEventAuditRefusesMissingOrClosedOriginalAppendResource(t *testing.T) {
	event := alerts.Event{V: 1, Event: alerts.SandboxError, Stream: "0123456789abcdef0123456789abcdef", Sequence: 1}
	if err := (*auditLog)(nil).writeEvent(event); err == nil {
		t.Fatal("an absent native append resource claimed persistence")
	}
	dir := t.TempDir()
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	audit := openAudit(filepath.Join(dir, "audit.jsonl"), slog.New(slog.DiscardHandler))
	audit.close()
	if err := audit.writeEvent(event); err == nil {
		t.Fatal("a closed native append resource claimed persistence")
	}
}
