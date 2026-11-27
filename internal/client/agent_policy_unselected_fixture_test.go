package client

import (
	"encoding/json"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func agentUnselectedID(n int) string {
	var id [32]byte
	for i := range id {
		id[i] = 'a'
	}
	if n%2 != 0 {
		id[0] = 'r' // Put unrelated rows before and after the selection.
	}
	for i := range 8 {
		id[len(id)-1-i] = api.SessionIDAlphabet[n&31]
		n >>= 5
	}
	return string(id[:])
}

func agentDenseProjectionFixture(tb testing.TB, schema, count int, edit func(int, string, map[string]any)) []byte {
	tb.Helper()
	rows := map[string]any{projectionSession: projectionRow()}
	if schema == 1 {
		delete(rows[projectionSession].(map[string]any), "wall_intent")
	}
	for n := range count {
		id, row := agentUnselectedID(n), projectionRow()
		row["session_id"] = id
		if schema == 1 {
			delete(row, "wall_intent")
		} else if n%3 != 2 {
			row["ssh_agent"] = false
			row["wall_intent"] = map[string]any{"level": "process", "exposed": []string{"A_KEY", "Z_KEY"}}
			row["secrets"] = []string{"A_KEY"}
			if schema == 3 {
				row["lineage"] = map[string]any{"version": 1, "parent": "parent", "root": "origin", "allowed_names": []string{"A_KEY", "Z_KEY"}}
				if n%3 == 1 {
					row["lineage"].(map[string]any)["version"] = 2
					row["lineage"].(map[string]any)["future"] = map[string]any{"opaque": "kept"}
				}
			}
		}
		if edit != nil {
			edit(n, id, row)
		}
		rows[id] = row
	}
	raw, err := json.Marshal(map[string]any{"schema": schema, "host": "box", "sessions": rows})
	if err != nil {
		tb.Fatal(err)
	}
	return raw
}

func projectionSingleRowSpans(t *testing.T, input []byte) (*api.JSONProjection, api.JSONField) {
	t.Helper()
	p, err := api.NewJSONBytesProjection(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	root, err := p.ObjectRoot()
	if err != nil {
		t.Fatal(err)
	}
	var row api.JSONField
	if err := p.Object(root, nil, func(field api.JSONField) error { row = field; return nil }); err != nil {
		t.Fatal(err)
	}
	return p, row
}
