package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/alerts"
)

func TestEventLines(t *testing.T) {
	out, stderr := captureOutput(t, func() {
		for _, ev := range []alerts.Event{
			{Event: alerts.NeedsInput, Summary: "job on box needs you"},
			{Event: alerts.SandboxReady, Summary: "job on box is ready"},
			{Event: alerts.SandboxStopped, Reason: "max_run", Summary: "job on box stopped (max_run)"},
		} {
			ev.Time = "2026-11-15T10:00:00Z"
			ev.Session.ID = "abcd2345"
			if err := writeEvent("box", ev, false); err != nil {
				t.Fatal(err)
			}
		}
	})
	want, err := os.ReadFile(filepath.Join("testdata", "events.golden"))
	if err != nil || out != string(want) || stderr != "" {
		t.Fatalf("event lines stdout=%q stderr=%q err=%v", out, stderr, err)
	}
}

func TestEventJSONIsOneRawVersionedEventPerLine(t *testing.T) {
	ev := alerts.Event{V: 1, Event: alerts.NeedsInput, Host: "box", Session: alerts.Session{ID: "abcd2345", Name: "job\x1bc"}, Time: "2026-11-15T10:00:00Z", Summary: "job\x1bc needs you"}
	out, stderr := captureOutput(t, func() {
		if err := writeEvent("box", ev, true); err != nil {
			t.Fatal(err)
		}
	})
	var decoded alerts.Event
	if err := json.Unmarshal([]byte(out), &decoded); err != nil || decoded.V != 1 || decoded.Session.Name != ev.Session.Name || stderr != "" {
		t.Fatalf("event JSON stdout=%q stderr=%q err=%v", out, stderr, err)
	}
	if len(out) == 0 || out[len(out)-1] != '\n' {
		t.Fatal("event JSON has no record delimiter")
	}
}
