package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

const projectionSession = "abcd2345"

func projectionRow() map[string]any {
	return map[string]any{
		"session_id": projectionSession, "host_id": strings.Repeat("a", 32), "ceiling": "own",
		"ssh_agent": true, "created_at": "2026-11-14T12:00:00Z", "request": "generated request",
		"wall_intent": map[string]any{"level": "none", "exposed": []string{"A_KEY", "Z_KEY"}},
	}
}

func projectionState(t *testing.T, schema int, row map[string]any, extra map[string]any) []byte {
	t.Helper()
	sessions := map[string]any{projectionSession: row}
	for name, value := range extra {
		sessions[name] = value
	}
	raw, err := json.Marshal(map[string]any{"schema": schema, "host": "box", "sessions": sessions})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// This comparison requires the approved P1.12 opaque field join in the
// existing decoder. It does not import or exercise a policy engine.
func compareAgentProjection(t *testing.T, input []byte) {
	t.Helper()
	full, originalErr := decodeSandboxFile(input, Host{Name: "box"})
	var want *agentSandboxPolicy
	if originalErr == nil {
		if row, ok := full.sessions[projectionSession]; ok {
			policy, err := decodeSandbox(projectionSession, row, full.schema)
			if err != nil {
				t.Fatal(err)
			}
			want = &agentSandboxPolicy{FutureLineage: policy.FutureLineage, HostID: policy.HostID, Ceiling: policy.Ceiling, SSHAgent: policy.SSHAgent, CreationWall: policy.CreationWall}
		}
	}
	got, projectedErr := projectAgentSandboxPolicy(t.Context(), bytes.NewReader(input), int64(len(input)), "box", projectionSession)
	if (originalErr == nil) != (projectedErr == nil) || originalErr == nil && !reflect.DeepEqual(want, got) {
		t.Fatalf("agent projection changed existing state validity/facts: original=%v projected=%v want=%+v got=%+v", originalErr, projectedErr, want, got)
	}
}

func TestAgentPolicyProjectionMatchesEveryKnownRowValidation(t *testing.T) {
	cases := []struct {
		name string
		edit func(map[string]any)
	}{
		{"valid", func(map[string]any) {}},
		{"optional nulls", func(row map[string]any) { row["host_id"], row["ssh_agent"], row["secrets"] = nil, nil, nil }},
		{"tier whitespace", func(row map[string]any) { row["ceiling"] = "\u2003\t ThRoWaWaY \n\u0085" }},
		{"empty host identity", func(row map[string]any) { row["host_id"] = "" }},
		{"uppercase host identity", func(row map[string]any) { row["host_id"] = strings.Repeat("A", 32) }},
		{"bad session id", func(row map[string]any) { row["session_id"] = "different" }},
		{"empty request", func(row map[string]any) { row["request"] = "" }},
		{"null request", func(row map[string]any) { row["request"] = nil }},
		{"null time", func(row map[string]any) { row["created_at"] = nil }},
		{"zero time", func(row map[string]any) { row["created_at"] = "0001-01-01T00:00:00Z" }},
		{"bad secret name", func(row map[string]any) { row["secrets"] = []string{"VALID", "NOT-VALID"} }},
		{"secret duplicate permitted", func(row map[string]any) { row["secrets"] = []string{"VALID", "VALID"} }},
		{"null wall", func(row map[string]any) { row["wall_intent"] = nil }},
		{"missing wall", func(row map[string]any) { delete(row, "wall_intent") }},
		{"agent behind wall", func(row map[string]any) { row["wall_intent"] = map[string]any{"level": "process"} }},
		{"nonagent process", func(row map[string]any) {
			row["ssh_agent"] = false
			row["wall_intent"] = map[string]any{"level": "process"}
		}},
		{"null exposures", func(row map[string]any) { row["wall_intent"] = map[string]any{"level": "none", "exposed": nil} }},
		{"unsorted exposures", func(row map[string]any) {
			row["wall_intent"] = map[string]any{"level": "none", "exposed": []string{"B", "A"}}
		}},
		{"reserved exposure", func(row map[string]any) {
			row["wall_intent"] = map[string]any{"level": "none", "exposed": []string{"SSH_AUTH_SOCK"}}
		}},
		{"opaque sources", func(row map[string]any) { row["policy_sources"] = json.RawMessage(`{"Future":{"x":1,"x":2}}`) }},
		{"opaque null", func(row map[string]any) { row["policy_sources"] = nil }},
		{"alias agent", func(row map[string]any) { row["SSH_AGENT"] = true }},
		{"alias sources", func(row map[string]any) { row["Policy_Sources"] = true }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			row := projectionRow()
			test.edit(row)
			compareAgentProjection(t, projectionState(t, 2, row, nil))
		})
	}
	for _, schema := range []int{1, 2} {
		row := projectionRow()
		delete(row, "wall_intent")
		compareAgentProjection(t, projectionState(t, schema, row, nil))
	}
}

func TestAgentPolicyProjectionValidatesUnselectedSessionsAndTrailingInput(t *testing.T) {
	for _, id := range []string{"other234", "UPPER", "with-dash", strings.Repeat("a", 33)} {
		other := projectionRow()
		other["session_id"] = id
		compareAgentProjection(t, projectionState(t, 2, projectionRow(), map[string]any{id: other}))
		other["ceiling"] = "unrecognised"
		compareAgentProjection(t, projectionState(t, 2, projectionRow(), map[string]any{id: other}))
	}
	valid := projectionState(t, 2, projectionRow(), nil)
	for _, suffix := range []string{" false", "\x00", " {\"other\":1}"} {
		compareAgentProjection(t, append(bytes.Clone(valid), suffix...))
	}
	for _, replacement := range []string{
		`"ssh_agent":true,"ssh_agent":false`, `"ssh_agent":true,"\u0073sh_agent":false`,
		`"wall_intent":{"level":"none","Level":"none"}`, `"wall_intent":{"level":"none","future":1,"future":2}`,
	} {
		var input []byte
		if strings.HasPrefix(replacement, `"ssh_agent"`) {
			input = bytes.Replace(valid, []byte(`"ssh_agent":true`), []byte(replacement), 1)
		} else {
			input = bytes.Replace(valid, []byte(`"wall_intent":{"exposed":["A_KEY","Z_KEY"],"level":"none"}`), []byte(replacement), 1)
		}
		if bytes.Equal(input, valid) {
			t.Fatal("malformed-state fixture did not change its source")
		}
		compareAgentProjection(t, input)
	}
}

func TestAgentPolicyProjectionLargeLegalScalarSemantics(t *testing.T) {
	for _, field := range []string{"request", "ceiling", "secrets", "exposed", "created_at", "policy_sources"} {
		t.Run(field, func(t *testing.T) {
			row := projectionRow()
			large := strings.Repeat("A", 4<<20)
			switch field {
			case "request":
				row[field] = large
			case "ceiling":
				row[field] = strings.Repeat("\u2003", (4<<20)/3) + "OWN" + strings.Repeat(" ", 1024)
			case "secrets":
				row[field] = []string{large, "SSH_AUTH_SOCK"}
			case "exposed":
				row["wall_intent"] = map[string]any{"level": "none", "exposed": []string{large, "B"}}
			case "created_at":
				row[field] = "2026-11-14T1:02:03," + strings.Repeat("1", 4<<20) + "+24:60"
			case "policy_sources":
				row[field] = map[string]any{"future": large}
			}
			// This is differential validity evidence, not an actor memory
			// proof. The reference reader deliberately materialises data.
			compareAgentProjection(t, projectionState(t, 2, row, nil))
		})
	}
}

func TestAgentPolicyProjectedTimeUsesGoFallbackSemantics(t *testing.T) {
	for _, value := range []string{
		"2026-11-14T12:01:02Z", "2026-11-14T1:01:02Z", "2026-11-14T12:01:02,123456789123Z",
		"2026-11-14T12:01:02.123456789123+24:60", "2026-11-14T12:01:02.000000000000Z",
		"0001-01-01T00:00:00.000000000000Z", "2026-11-14T12:01:02.Z",
		"2026-11-14T12:01:02.123badZ", "2026-11-14T12.123456789123:01:02Z",
		"2026-11-14T12:01:02Zextra", "2026-11-14T12:01:02.123456789123+99:99",
	} {
		row := projectionRow()
		row["created_at"] = value
		compareAgentProjection(t, projectionState(t, 2, row, nil))
	}
}

func TestAgentPolicySnapshotRejectsReplacementAndRetainsCanonicalLock(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	host := Host{Name: "box"}
	path := filepath.Join(StateDir(), "hosts", HostFileKey(host.Name), "sandboxes.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	valid := projectionState(t, 2, projectionRow(), nil)
	if err := os.WriteFile(path, valid, 0o600); err != nil {
		t.Fatal(err)
	}
	err := withAgentPolicySnapshot(t.Context(), host, func(source io.ReaderAt, size int64, exists bool) error {
		if !exists {
			t.Fatal("fixture snapshot missing")
		}
		shared := stateProcessMutex([]string{"hosts", HostFileKey(host.Name)}, "sandboxes.json")
		if shared.TryLock() {
			shared.Unlock()
			t.Fatal("agent snapshot did not hold the canonical state mutex")
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := withStateFile(ctx, []string{"hosts", HostFileKey(host.Name)}, "sandboxes.json", true,
			func([]byte, bool) ([]byte, error) { t.Fatal("cancelled competing writer entered"); return nil, nil }); !errors.Is(err, context.Canceled) {
			t.Fatalf("canonical competing writer lost cancellation: %v", err)
		}
		if _, err := projectAgentSandboxPolicy(t.Context(), source, size, host.Name, projectionSession); err != nil {
			return err
		}
		replacement := path + ".replacement"
		if err := os.WriteFile(replacement, valid, 0o600); err != nil {
			return err
		}
		return os.Rename(replacement, path)
	})
	if err == nil {
		t.Fatal("replaced source path returned an agent permit")
	}
}

func FuzzAgentPolicyProjectionParity(f *testing.F) {
	f.Add([]byte(`{"schema":2,"host":"box","sessions":{}}`))
	f.Add([]byte(fmt.Sprintf(`{"schema":1,"host":"box","sessions":{"%s":{"session_id":"%s","ceiling":"own","request":"r","created_at":"2026-11-14T00:00:00Z"}}}`, projectionSession, projectionSession)))
	f.Fuzz(func(t *testing.T, input []byte) { compareAgentProjection(t, input) })
}

func FuzzAgentProjectedCreationTimeParity(f *testing.F) {
	for _, seed := range []string{
		`"2026-11-14T12:01:02Z"`, `"2026-11-14T1:01:02,123456789123+24:60"`,
		`"0001-01-01T00:00:00.000000000000Z"`, `"2026-11-14T12\u003a01:02Z"`, `null`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, input []byte) {
		var reference time.Time
		originalErr := json.Unmarshal(input, &reference)
		projection, err := api.NewJSONProjection(t.Context(), bytes.NewReader(input), int64(len(input)))
		if err == nil {
			var span api.JSONSpan
			span, err = projection.Root()
			if err == nil {
				err = projectedCreationTime(projection, span)
			}
		}
		if (originalErr == nil && !reference.IsZero()) != (err == nil) {
			t.Fatalf("projected time changed Go validity/zero semantics: original=%v projected=%v", originalErr, err)
		}
	})
}
