package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestContextSandboxDecoderPreservesKnownAndFutureGrammar(t *testing.T) {
	for _, mode := range []string{"schema1", "schema2", "known-lineage", "future-lineage", "bad-version", "bad-secret"} {
		t.Run(mode, func(t *testing.T) {
			row, schema := projectionRow(), 2
			row["policy_sources"] = json.RawMessage(`{ "future" : [1,  2, {"duplicate":1,"duplicate":2}] }`)
			switch mode {
			case "schema1":
				schema = 1
				delete(row, "wall_intent")
			case "known-lineage":
				schema, row["ssh_agent"] = 3, false
				row["wall_intent"] = map[string]any{"level": "process"}
				row["secrets"] = []string{"A_KEY"}
				row["lineage"] = map[string]any{"version": 1, "parent": "def234gh", "root": "def234gh", "allowed_names": []string{"A_KEY", "Z_KEY"}}
			case "future-lineage":
				schema, row["ssh_agent"] = 3, false
				row["lineage"] = map[string]any{"version": 2, "future": []any{nil, 1, "unknown"}}
			case "bad-version":
				schema, row["ssh_agent"] = 3, false
				row["lineage"] = map[string]any{"version": "2"}
			case "bad-secret":
				row["secrets"] = []string{"INVALID-NAME"}
			}
			raw := projectionState(t, schema, row, nil)
			current, newErr := decodeSandboxFileContext(t.Context(), raw, Host{Name: "box"})
			invalid := mode == "bad-version" || mode == "bad-secret"
			if (newErr != nil) != invalid {
				t.Fatalf("context changed accepted schema or row validity: %v", newErr)
			}
			if newErr != nil {
				return
			}
			policy, err := decodeSandboxContext(t.Context(), projectionSession, current.sessions[projectionSession], schema)
			if err != nil {
				t.Fatal(err)
			}
			if current.schema != schema || policy.FutureLineage != (mode == "future-lineage") {
				t.Fatal("the contextual decoder changed schema or future authority")
			}
			if mode == "future-lineage" && (policy.lineage != nil || len(localSandboxNames(&policy)) != 0) {
				t.Fatal("future lineage gained a known local allowance")
			}
			if mode == "known-lineage" && (policy.lineage == nil || policy.lineage.Root != "def234gh" || policy.CreationWall != "process") {
				t.Fatal("known immutable lineage was lost")
			}
			fields, err := sandboxObject(current.sessions[projectionSession])
			if err != nil || !bytes.Equal(policy.PolicySources, fields["policy_sources"]) {
				t.Fatal("the contextual decoder changed opaque source bytes")
			}
		})
	}
}

type sandboxCancelAfterChecks struct {
	context.Context
	cancel context.CancelFunc
	left   int
}

func (c *sandboxCancelAfterChecks) Err() error {
	if c.left > 0 {
		c.left--
		if c.left == 0 {
			c.cancel()
		}
	}
	return c.Context.Err()
}

func TestContextSandboxDecoderCancelsInsideOpaqueStructuralScan(t *testing.T) {
	row := projectionRow()
	row["policy_sources"] = map[string]any{"future": strings.Repeat("x", 1<<20)}
	raw := projectionState(t, 2, row, nil)
	base, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx := &sandboxCancelAfterChecks{Context: base, cancel: cancel, left: 16}
	if _, err := decodeSandboxFileContext(ctx, raw, Host{Name: "box"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("the parser ignored its parent during the body scan: %v", err)
	}
	if ctx.left != 0 {
		t.Fatal("the cancellation source was not reached inside the parser")
	}
	if _, err := decodeSandboxFileContext(ctx, []byte(`{"broken":`), Host{Name: "box"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled entry reported data validity instead: %v", err)
	}
}
