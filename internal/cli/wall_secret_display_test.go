package cli

import (
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

func TestWallStartNotesUseKnownModesAndLeaveUnknownDeliveryUnknown(t *testing.T) {
	session := client.Session{Wall: &api.WallInfo{Level: "gvisor"}, Secrets: []string{"BOUND", "VISIBLE"}, SecretSummary: &api.SessionSecretSummary{Version: api.SessionSecretSummaryV1, Generation: strings.Repeat("a", 32), Secrets: map[string]api.SecretStatus{"BOUND": {Mode: api.SecretModeBound, Armed: true}, "VISIBLE": {Mode: api.SecretModeExposed, Armed: true}}}}
	_, stderr := captureOutput(t, func() { wallStartNotes(Root(), session) })
	if strings.Contains(stderr, "exposed: BOUND") || !strings.Contains(stderr, "exposed: VISIBLE") {
		t.Fatalf("bound/exposed notes were confused: %q", stderr)
	}
	session.SecretSummary = nil
	_, stderr = captureOutput(t, func() { wallStartNotes(Root(), session) })
	if strings.Contains(stderr, "exposed:") || !strings.Contains(stderr, "secret delivery unknown: BOUND") || !strings.Contains(stderr, "secret delivery unknown: VISIBLE") {
		t.Fatalf("absent metadata implied exposure: %q", stderr)
	}
}
