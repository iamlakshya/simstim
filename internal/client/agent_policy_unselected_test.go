package client

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestAgentProjectionDenseUnselectedRowsMatchFullReader(t *testing.T) {
	for _, schema := range []int{1, 2, 3} {
		input := agentDenseProjectionFixture(t, schema, 4096, nil)
		if _, err := decodeSandboxFile(input, Host{Name: "box"}); err != nil {
			t.Fatalf("dense schema%d fixture is not valid: %v", schema, err)
		}
		compareAgentProjection(t, input)
	}
}

func TestAgentProjectionDenseUnselectedInvalidRowsStillRefuse(t *testing.T) {
	cases := []struct {
		name string
		edit func(string, map[string]any)
	}{
		{"session mismatch", func(_ string, row map[string]any) { row["session_id"] = "different" }},
		{"uppercase host ID", func(_ string, row map[string]any) { row["host_id"] = strings.Repeat("A", 32) }},
		{"short host ID", func(_ string, row map[string]any) { row["host_id"] = "a" }},
		{"host alias", func(_ string, row map[string]any) { row["HOST_ID"] = row["host_id"] }},
		{"wall alias", func(_ string, row map[string]any) { row["Wall_Intent"] = row["wall_intent"] }},
		{"lineage alias", func(_ string, row map[string]any) { row["Lineage"] = row["lineage"] }},
		{"wall level", func(_ string, row map[string]any) { row["wall_intent"].(map[string]any)["level"] = "unknown" }},
		{"lineage self parent", func(id string, row map[string]any) { row["lineage"].(map[string]any)["parent"] = id }},
		{"escaped lineage self parent", func(id string, row map[string]any) {
			row["lineage"].(map[string]any)["parent"] = json.RawMessage(`"\u0072` + id[1:] + `"`)
		}},
		{"lineage self root", func(id string, row map[string]any) { row["lineage"].(map[string]any)["root"] = id }},
		{"lineage missing host", func(_ string, row map[string]any) { delete(row, "host_id") }},
		{"future agent", func(_ string, row map[string]any) {
			row["ssh_agent"] = true
			row["wall_intent"] = map[string]any{"level": "none"}
			row["lineage"].(map[string]any)["version"] = 2
		}},
		{"zero time", func(_ string, row map[string]any) { row["created_at"] = "0001-01-01T00:00:00Z" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := agentDenseProjectionFixture(t, 3, 512, func(n int, id string, row map[string]any) {
				if n == 507 { // Known lineage after the selected key.
					tc.edit(id, row)
				}
			})
			compareAgentProjection(t, input)
			if _, err := projectAgentSandboxPolicy(t.Context(), bytes.NewReader(input), int64(len(input)), "box", projectionSession); err == nil {
				t.Fatal("an unselected row stopped being a full-file validity barrier")
			}
		})
	}
}

func TestAgentProjectionUnselectedEscapesAndGoTimeParity(t *testing.T) {
	for _, stamp := range []string{
		"2026-11-14T12:01:02Z",
		"2026-11-14T1:01:02,123456789123+24:60", "2026-11-14T12:01:02.123456789123+24:60",
		"0001-01-01T00:00:00.000000000000Z", "2026-11-14T12:01:02Zextra",
	} {
		input := agentDenseProjectionFixture(t, 3, 32, func(n int, _ string, row map[string]any) {
			if n == 31 {
				row["created_at"] = stamp
				row["host_id"] = json.RawMessage(`"\u0061aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`)
			}
		})
		id := agentUnselectedID(31)
		encoded, _ := json.Marshal(id)
		escaped := []byte(`"\u0072` + id[1:] + `"`)
		input = bytes.ReplaceAll(input, encoded, escaped)
		compareAgentProjection(t, input)
		if stamp == "2026-11-14T12:01:02Z" {
			if _, err := decodeSandboxFile(input, Host{Name: "box"}); err != nil {
				t.Fatalf("escaped canonical IDs are not a valid fixture: %v", err)
			}
		}
	}
}

func TestAgentProjectionUnselectedSessionIDUsesDecodedEquality(t *testing.T) {
	input := agentDenseProjectionFixture(t, 3, 32, func(n int, id string, row map[string]any) {
		if n == 30 {
			row["session_id"] = json.RawMessage(`"\u0061` + id[1:] + `"`)
		}
	})
	if _, err := decodeSandboxFile(input, Host{Name: "box"}); err != nil {
		t.Fatal(err)
	}
	compareAgentProjection(t, input)
}

func TestAgentProjectionUnselectedRetainsNoStringsAndStillChecksLineage(t *testing.T) {
	for _, version := range []int{1, 2} {
		row := projectionRow()
		row["ssh_agent"] = false
		row["wall_intent"] = map[string]any{"level": "process", "exposed": []string{"A_KEY"}}
		row["secrets"] = []string{"A_KEY"}
		row["lineage"] = map[string]any{"version": version, "parent": "parent", "root": "origin", "allowed_names": []string{"A_KEY"}}
		raw, err := json.Marshal(map[string]any{projectionSession: row})
		if err != nil {
			t.Fatal(err)
		}
		p, field := projectionSingleRowSpans(t, raw)
		facts, err := projectAgentRow(p, field.Value, field.Name, 3, false)
		if err != nil || facts.HostID != "" || facts.CreationWall != "" || facts.FutureLineage != (version == 2) {
			t.Fatalf("unselected strings retained or validation facts lost: %+v %v", facts, err)
		}
	}
}

func TestAgentProjectionSelectedStringsOwnTheirBytes(t *testing.T) {
	row := projectionRow()
	row["ssh_agent"] = false
	row["wall_intent"] = map[string]any{"level": "process"}
	input, err := json.Marshal(map[string]any{projectionSession: row})
	if err != nil {
		t.Fatal(err)
	}
	p, field := projectionSingleRowSpans(t, input)
	facts, err := projectAgentRow(p, field.Value, field.Name, 2, true)
	if err != nil {
		t.Fatal(err)
	}
	replaced := bytes.ReplaceAll(input, []byte(strings.Repeat("a", 32)), []byte(strings.Repeat("b", 32)))
	replaced = bytes.ReplaceAll(replaced, []byte("process"), []byte("microvm"))
	copy(input, replaced)
	if facts.HostID != strings.Repeat("a", 32) || facts.CreationWall != "process" {
		t.Fatal("selected strings alias the borrowed projection source")
	}
}

func BenchmarkAgentPolicyProjectionDenseUnselected(b *testing.B) {
	for _, count := range []int{0, 4096} {
		name := "selected"
		if count != 0 {
			name = "selected-and-4096-unselected"
		}
		b.Run(name, func(b *testing.B) {
			input := agentDenseProjectionFixture(b, 3, count, nil)
			source := bytes.NewReader(input)
			b.ReportAllocs()
			b.SetBytes(int64(len(input)))
			b.ResetTimer()
			for range b.N {
				facts, err := projectAgentSandboxPolicy(b.Context(), source, int64(len(input)), "box", projectionSession)
				if err != nil || facts == nil {
					b.Fatalf("projection failed: %+v %v", facts, err)
				}
			}
		})
	}
}
