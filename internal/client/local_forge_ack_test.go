//go:build darwin || linux

package client

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/tomlinspect"
)

func forgeAckInspectionFixture(t *testing.T, row, other map[string]any, candidates map[string]bool, change func(*sandboxReceiptScope)) (localForgeAckFacts, error) {
	t.Helper()
	conn, mode := agentCombinedInspectionFixture(t, nil)
	if err := os.WriteFile(sandboxStatePath(conn.Host), projectionState(t, 4, row, other), 0600); err != nil {
		t.Fatal(err)
	}
	// Only the out-of-band oracle materialises this fixture's tiny scope.
	encoded, err := json.Marshal(row["delivery_receipt"])
	if err != nil {
		t.Fatal(err)
	}
	var receipt sandboxDeliveryReceipt
	if err := json.Unmarshal(encoded, &receipt); err != nil {
		t.Fatal(err)
	}
	selection := localForgeAckSelection{facts: localForgeAckFacts{scope: receipt.Scope}, candidates: candidates}
	if change != nil {
		change(&selection.facts.scope)
	}
	snapshot, err := openAgentSettingsSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := snapshot.close(); err != nil {
			t.Error(err)
		}
	}()
	err = withHostStateReadContext(t.Context(), conn.Host, func(owned context.Context) error {
		return withStateTransaction(owned, []string{"hosts", HostFileKey(conn.Host.Name)}, "sandboxes.json", false, false, func(tx *stateFileTxn) error {
			scope, err := newAgentMetadataScope(mode, snapshot, tx)
			if err != nil {
				return err
			}
			defer scope.close()
			return mode.inspector.engine.WithSources(scope.ctx, scope.descriptors(), func(h *tomlinspect.Inspection) error {
				defer scope.finishCachedWork()
				if err := h.InspectTOML(0, &mode.inspector.visitor); err != nil {
					return err
				}
				_, err := inspectAgentSandboxPoliciesForForge(h, "box", projectionSession, &selection)
				return err
			})
		})
	})
	return selection.facts, err
}

func TestForgeAckInspectionUsesOnlySelectedLocalAcknowledgedNames(t *testing.T) {
	row := receiptProjectionFixtureRow(t)
	for _, test := range []struct {
		name       string
		candidates map[string]bool
		want       string
	}{{"exact local", map[string]bool{"A": true, "KEY": true, "Z": true}, "KEY"},
		{"case differs", map[string]bool{"key": true}, ""},
		{"absent local", map[string]bool{"OTHER": true}, ""},
		{"anonymous", nil, ""}} {
		t.Run(test.name, func(t *testing.T) {
			facts, err := forgeAckInspectionFixture(t, row, nil, test.candidates, nil)
			if err != nil {
				t.Fatal(err)
			}
			if facts.name != test.want || facts.revision != strings.Repeat("e", 32) || facts.creation != strings.Repeat("f", 32) || facts.ceiling != TierOwn {
				t.Fatal("selected ACK facts differ from the full receipt oracle")
			}
		})
	}
}

func TestForgeAckInspectionRejectsChangedSourceAndMalformedUnselectedReceipt(t *testing.T) {
	row := receiptProjectionFixtureRow(t)
	if _, err := forgeAckInspectionFixture(t, row, nil, nil, func(scope *sandboxReceiptScope) {
		scope.Source.Root.Size++
		scope.Source.Root.Modified = scope.Source.Root.Modified.Add(time.Hour)
	}); err != nil {
		t.Fatal("directory entry metadata changed the original Source identity", err)
	}
	if _, err := forgeAckInspectionFixture(t, row, nil, nil, func(scope *sandboxReceiptScope) { scope.Source.Generation = strings.Repeat("9", 32) }); err == nil {
		t.Fatal("another original Source matched the selected ACK")
	}
	other := projectionRow()
	other["session_id"], other["ssh_agent"] = "other234", false
	other["wall_intent"] = map[string]any{"level": "process"}
	other["delivery_receipt"] = json.RawMessage(`{"version":1}`)
	if _, err := forgeAckInspectionFixture(t, row, map[string]any{"other234": other}, nil, nil); err == nil {
		t.Fatal("a selected valid ACK hid a malformed unselected receipt")
	}
}

func TestForgeAckFutureAndKnownSemanticFailureNeverYieldTinyReceipt(t *testing.T) {
	for _, test := range []struct {
		name                                                  string
		unknown, sourceFuture, invalid, opaqueVersion, future bool
	}{
		{name: "known semantic failure", invalid: true},
		{name: "future receipt", unknown: true, future: true},
		{name: "future plus known semantic failure", unknown: true, invalid: true},
		{name: "future source plus known semantic failure", sourceFuture: true, invalid: true},
		{name: "opaque future version", opaqueVersion: true, future: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			row := receiptProjectionFixtureRow(t)
			receipt := row["delivery_receipt"].(map[string]any)
			if test.unknown {
				receipt["future_receipt_fact"] = true
			}
			if test.sourceFuture {
				scope := receipt["scope"].(map[string]any)
				scope["source"].(map[string]any)["version"] = 2
			}
			if test.invalid {
				state := receipt["state"].(map[string]any)
				intent := state["intent"].(map[string]any)
				intent["modes"] = map[string]any{"KEY": "bound"}
				// A recognized v1 bound mode without its binding stays a
				// known semantic failure even beside an unknown outer field.
			}
			if test.opaqueVersion {
				receipt["version"] = 2
				receipt["revision"] = ""
				receipt["state"] = json.RawMessage(`{"future_format":{"duplicate":1,"duplicate":2}}`)
			}
			// The full and FD codecs must agree before the actual combined
			// source-backed producer decides whether tiny facts can escape.
			compareReceiptProjection(t, row, !test.invalid)
			facts, err := forgeAckInspectionFixture(t, row, nil, map[string]bool{"KEY": true}, nil)
			if err == nil || facts.revision != "" || facts.name != "" {
				t.Fatal("incomplete semantic validation produced selected ACK facts")
			}
			var refused, expected *UserError
			if !errors.As(err, &refused) || !errors.As(futureSandboxReceiptError(), &expected) || (refused.Msg == expected.Msg) != test.future {
				t.Fatalf("known failure/future classification changed: future=%v err=%v", test.future, err)
			}
		})
	}
}
