package client

import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"testing"
)

func TestLineageReservationKeepsOpaqueCreationBytesAndCannotGrow(t *testing.T) {
	h, parent, _, _ := sharedGrantFixture(t)
	parent.wallIntent = json.RawMessage(`{ "level":"process", "exposed":["API_KEY"], "future":{"x":1,"x":2} }`)
	if err := withSandboxFile(t.Context(), h, true, func(f *sandboxFile) error {
		fields, err := sandboxObject(f.sessions[parent.SessionID])
		if err != nil {
			return err
		}
		fields["wall_intent"] = parent.wallIntent
		f.sessions[parent.SessionID], err = marshalSandboxObject(fields)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	child := sharedGrantChild(t, h, parent, "def234gh", []string{"API_KEY"}, TierOwn)
	for _, edit := range []func(*SandboxPolicy){
		func(p *SandboxPolicy) { p.lineage.Allowed = append(p.lineage.Allowed, "OTHER_KEY") },
		func(p *SandboxPolicy) { p.lineage.Parent = "uvw234xy" },
		func(p *SandboxPolicy) { p.lineage.Root = "uvw234xy" },
		func(p *SandboxPolicy) { p.SSHAgent = true },
		func(p *SandboxPolicy) { p.PolicySources = json.RawMessage(`{"new":true}`) },
	} {
		changed := cloneSandboxPolicy(&child)
		edit(changed)
		if err := reserveSandbox(t.Context(), h, *changed); err == nil {
			t.Fatal("a derived retry changed inherited authority")
		}
	}
	saved, err := LoadSandboxPolicy(t.Context(), h, child.SessionID)
	if err != nil || !bytes.Equal(saved.wallIntent, parent.wallIntent) || !bytes.Equal(saved.PolicySources, parent.PolicySources) {
		t.Fatalf("raw inherited future metadata changed: %+v %v", saved, err)
	}
	if err := allowResumeSecretNames(t.Context(), h, parent.HostID, TierOwn, LoadSettings(), []string{"OTHER_KEY"}, saved); err == nil {
		t.Fatal("a derived resume expanded names from current settings")
	}
}

func TestLineageSchemaBarrierAndMalformedRowsMatchAgentProjection(t *testing.T) {
	for _, schema := range []int{1, 2, 3, 4} {
		row := projectionRow()
		row["ssh_agent"] = false
		row["wall_intent"] = map[string]any{"level": "process", "exposed": []string{"A_KEY", "Z_KEY"}}
		row["secrets"] = []string{"A_KEY"}
		row["lineage"] = map[string]any{"version": 1, "parent": "def234gh", "root": "def234gh", "allowed_names": []string{"A_KEY", "Z_KEY"}}
		raw := projectionState(t, schema, row, nil)
		_, err := decodeSandboxFile(raw, Host{Name: "box"})
		if (err == nil) != (schema == 3) {
			t.Fatalf("schema%d lineage validity=%v", schema, err)
		}
		compareAgentProjection(t, raw)
	}
	cases := []struct {
		name string
		edit func(map[string]any)
	}{
		{"valid", func(map[string]any) {}},
		{"null", func(row map[string]any) { row["lineage"] = nil }},
		{"missing names", func(row map[string]any) { delete(row["lineage"].(map[string]any), "allowed_names") }},
		{"null names", func(row map[string]any) { row["lineage"].(map[string]any)["allowed_names"] = nil }},
		{"self parent", func(row map[string]any) { row["lineage"].(map[string]any)["parent"] = projectionSession }},
		{"self root", func(row map[string]any) { row["lineage"].(map[string]any)["root"] = projectionSession }},
		{"foreign name", func(row map[string]any) { row["secrets"] = []string{"OTHER_KEY"} }},
		{"unsorted actual names", func(row map[string]any) { row["secrets"] = []string{"Z_KEY", "A_KEY"} }},
		{"duplicate actual names", func(row map[string]any) { row["secrets"] = []string{"A_KEY", "A_KEY"} }},
		{"unsorted allowance", func(row map[string]any) {
			row["lineage"].(map[string]any)["allowed_names"] = []string{"Z_KEY", "A_KEY"}
		}},
		{"duplicate allowance", func(row map[string]any) {
			row["lineage"].(map[string]any)["allowed_names"] = []string{"A_KEY", "A_KEY"}
		}},
		{"reserved agent socket", func(row map[string]any) { row["lineage"].(map[string]any)["allowed_names"] = []string{"SSH_AUTH_SOCK"} }},
		{"agent authority", func(row map[string]any) { row["ssh_agent"] = true }},
		{"missing identity", func(row map[string]any) { delete(row, "host_id") }},
		{"none wall", func(row map[string]any) { row["wall_intent"] = map[string]any{"level": "none"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := projectionRow()
			row["ssh_agent"], row["secrets"] = false, []string{"A_KEY"}
			row["wall_intent"] = map[string]any{"level": "process", "exposed": []string{"A_KEY", "Z_KEY"}}
			row["lineage"] = map[string]any{"version": 1, "parent": "def234gh", "root": "def234gh", "allowed_names": []string{"A_KEY", "Z_KEY"}}
			tc.edit(row)
			raw := projectionState(t, 3, row, nil)
			compareAgentProjection(t, raw)
			_, err := decodeSandboxFile(raw, Host{Name: "box"})
			if (err == nil) != (tc.name == "valid") {
				t.Fatalf("malformed lineage validity=%v", err)
			}
		})
	}
}

func TestSchema3KeepsOrdinaryAgentRowsAndRefusesUnrelatedBadLineage(t *testing.T) {
	row := projectionRow()
	compareAgentProjection(t, projectionState(t, 3, row, nil))
	other := projectionRow()
	other["session_id"], other["ssh_agent"] = "def234gh", false
	other["wall_intent"] = map[string]any{"level": "process"}
	other["lineage"] = map[string]any{"version": 1, "parent": projectionSession, "root": projectionSession, "allowed_names": []string{"B", "A"}}
	raw := projectionState(t, 3, row, map[string]any{"def234gh": other})
	compareAgentProjection(t, raw)
	if _, err := projectAgentSandboxPolicy(t.Context(), bytes.NewReader(raw), int64(len(raw)), "box", projectionSession); err == nil {
		t.Fatal("malformed unrelated lineage bypassed the global agent gate")
	}
}

func TestLineageCopyDoesNotAliasMutableSourceSlices(t *testing.T) {
	h, p, _, _ := sharedGrantFixture(t)
	child := sharedGrantChild(t, h, p, "def234gh", []string{"API_KEY"}, TierOwn)
	copy := cloneSandboxPolicy(&child)
	copy.PolicySources[0], copy.wallIntent[0] = '[', '['
	copy.lineage.Allowed[0] = "OTHER_KEY"
	copy.Exposed[0] = "OTHER_KEY"
	if !slices.Equal(child.lineage.Allowed, []string{"API_KEY"}) || child.PolicySources[0] != '{' || child.wallIntent[0] != '{' {
		t.Fatal("an inherited snapshot aliases mutable caller state")
	}
	// Ordinary existing rows have no artificial lineage schema migration.
	path := sandboxStatePath(h)
	if raw, err := os.ReadFile(path); err != nil || !bytes.Contains(raw, []byte(`"schema":3`)) {
		t.Fatalf("derived state missing schema barrier: %v", err)
	}
}
