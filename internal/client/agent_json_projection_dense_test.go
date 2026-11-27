//go:build darwin || linux

package client

import (
	"fmt"
	"os"
	"testing"
)

// The fixture is streamed before the actual check. It does not reset an actor
// meter or create a second parser/history inside the production inspection.
func writeGuardedReverseSessionFixture(t *testing.T, path string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	write := func(format string, values ...any) {
		t.Helper()
		if _, err := fmt.Fprintf(file, format, values...); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"schema":4,"host":"box","sessions":{`)
	var padding [4096]byte
	for i := range padding {
		padding[i] = 'x'
	}
	for index := 127; index >= 0; index-- {
		if index != 127 {
			write(",")
		}
		id := []byte("rowaaaaa")
		value := index
		for position := len(id) - 1; position >= 3; position-- {
			id[position] = 'a' + byte(value%26)
			value /= 26
		}
		selected := index == 0
		name, wall := string(id), "process"
		if selected {
			name, wall = projectionSession, "none"
		}
		write(`%q:{"session_id":%q,"host_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","ceiling":"own","ssh_agent":%t,"created_at":"2026-11-14T12:00:00Z","request":"fixture","wall_intent":{"level":%q},"policy_sources":{"padding":"`, name, name, selected, wall)
		for range 5 {
			if _, err := file.Write(padding[:]); err != nil {
				t.Fatal(err)
			}
		}
		write(`"}}`)
	}
	write(`}}`)
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAgentCombinedGuardedReverseRowsMatchFullReader(t *testing.T) {
	conn, mode := agentCombinedInspectionFixture(t, nil)
	path := sandboxStatePath(conn.Host)
	writeGuardedReverseSessionFixture(t, path)
	// The full materialising reader is ONLY the out-of-band test oracle.
	// Production still traverses every known row/state on the one source.
	selected, err := LoadSandboxPolicy(t.Context(), conn.Host, projectionSession)
	if err != nil || selected == nil || !selected.SSHAgent || selected.CreationWall != "none" {
		t.Fatalf("reverse-row reference fixture is invalid: %v", err)
	}
	if err := conn.checkAgentPolicy(t.Context(), projectionSession, mode.inspector); err != nil {
		t.Fatal(err)
	}
}
