//go:build darwin || linux

package client

import (
	"encoding/json"
	"os"
	"testing"
)

func TestAgentCombinedInspectionKeepsFutureReceiptsOpaqueWithoutGrantingThem(t *testing.T) {
	for _, field := range []string{"delivery_receipt", "input_operation"} {
		for _, selected := range []bool{false, true} {
			name := field + "/other"
			if selected {
				name = field + "/selected"
			}
			t.Run(name, func(t *testing.T) {
				row := projectionRow()
				row[field] = json.RawMessage(`{"version":2,"future":{"duplicate":1,"duplicate":2}}`)
				var other map[string]any
				if !selected {
					row["session_id"], row["ssh_agent"] = "other234", false
					row["wall_intent"] = map[string]any{"level": "process"}
					other = map[string]any{"other234": row}
				}
				conn, mode := agentCombinedInspectionFixture(t, other)
				if selected {
					if err := os.WriteFile(sandboxStatePath(conn.Host), projectionState(t, 4, row, nil), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := LoadSandboxPolicy(t.Context(), conn.Host, projectionSession); err != nil {
					t.Fatalf("the full reference reader did not preserve the future envelope: %v", err)
				}
				err := conn.checkAgentPolicy(t.Context(), projectionSession, mode.inspector)
				if (err == nil) == selected {
					t.Fatalf("future receipt selection=%v, agent admission=%v", selected, err)
				}
			})
		}
	}
}
