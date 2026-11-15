//go:build darwin || linux

package client

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/agentbridge"
)

func agentCombinedInspectionFixture(t *testing.T, other map[string]any) (*Conn, *agentModeResources) {
	t.Helper()
	host := h5GrantState(t)
	for _, key := range []string{"XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
		t.Setenv(key, t.TempDir())
	}
	row := projectionRow()
	row["request"] = strings.Repeat("c", 32)
	path := sandboxStatePath(host)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, projectionState(t, 4, row, other), 0o600); err != nil {
		t.Fatal(err)
	}
	writeAgentSettingsFixture(t, "[secrets]\nKEY = 'printf never-run'\n")
	conn := &Conn{Host: host, hostID: strings.Repeat("a", 32), Features: []string{agentbridge.Feature}}
	return conn, agentModeForTest(t)
}

func TestAgentCombinedInspectionUsesRealSettingsAndSelectedFacts(t *testing.T) {
	conn, mode := agentCombinedInspectionFixture(t, nil)
	if err := conn.checkAgentPolicy(t.Context(), projectionSession, mode.inspector); err != nil {
		t.Fatal(err)
	}
	writeAgentSettingsFixture(t, "[secrets]\nKEY = 'printf never-run'\nBROKEN = {run = ''}\n")
	if err := conn.checkAgentPolicy(t.Context(), projectionSession, mode.inspector); err == nil {
		t.Fatal("an invalid unrelated secret definition passed the live agent grant")
	}
}

func TestAgentCombinedInspectionChecksUnselectedReceipts(t *testing.T) {
	for _, test := range []struct {
		name, field string
		body        json.RawMessage
	}{
		{"delivery header", "delivery_receipt", json.RawMessage(`{"version":1}`)},
		{"input header", "input_operation", json.RawMessage(`{"version":1}`)},
		{"delivery alias", "delivery_receipt", json.RawMessage(`{"version":1,"Version":2}`)},
		{"input alias", "input_operation", json.RawMessage(`{"version":1,"Version":2}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			other := projectionRow()
			other["session_id"], other["ssh_agent"] = "other234", false
			other["wall_intent"] = map[string]any{"level": "process"}
			other[test.field] = test.body
			conn, mode := agentCombinedInspectionFixture(t, map[string]any{"other234": other})
			if _, err := LoadSandboxPolicy(t.Context(), conn.Host, projectionSession); err == nil {
				t.Fatal("the full reference reader accepted a malformed receipt fixture")
			}
			if err := conn.checkAgentPolicy(t.Context(), projectionSession, mode.inspector); err == nil {
				t.Fatal("an unselected malformed receipt passed the live agent grant")
			}
		})
	}
}

func TestAgentCombinedInspectionPreservesOpaqueUnselectedBodies(t *testing.T) {
	other := projectionRow()
	other["session_id"], other["ssh_agent"] = "other234", false
	other["wall_intent"] = map[string]any{"level": "process"}
	other["policy_sources"] = json.RawMessage(`{"future":{"duplicate":1,"duplicate":2}}`)
	conn, mode := agentCombinedInspectionFixture(t, map[string]any{"other234": other})
	if err := conn.checkAgentPolicy(t.Context(), projectionSession, mode.inspector); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := conn.checkAgentPolicy(ctx, projectionSession, mode.inspector); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled grant lost its original cancellation: %v", err)
	}
}
