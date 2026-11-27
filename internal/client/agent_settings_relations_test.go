//go:build darwin || linux

package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/tomlinspect"
)

func agentReceiptRelationsFixture(t *testing.T, groups map[string]any, run func(*api.JSONProjection, api.JSONSpan, sandboxReceiptRelationPlan, *tomlinspect.Inspection) error) error {
	t.Helper()
	conn, mode := agentCombinedInspectionFixture(t, nil)
	row := projectionRow()
	// Index-only source data is opaque to the row grammar. This fixture
	// creates no receipt, state support or permission from these values.
	row["policy_sources"] = groups
	if err := os.WriteFile(sandboxStatePath(conn.Host), projectionState(t, 4, row, nil), 0o600); err != nil {
		t.Fatal(err)
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
	return withHostStateReadContext(t.Context(), conn.Host, func(owned context.Context) error {
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
				ctx, err := h.Context()
				if err != nil {
					return err
				}
				source, err := h.Source(1)
				if err != nil {
					return err
				}
				p, err := api.NewJSONProjection(ctx, source, source.Size())
				if err != nil || !p.MatchesSource(ctx, source, source.Size()) {
					return tomlinspect.ErrIndexInvariant
				}
				_, err = projectAgentSandboxPolicyProjection(ctx, p, conn.Host.Name, projectionSession, func(row, _ api.JSONSpan, _ int, _ *agentSandboxPolicy) error {
					var opaque api.JSONSpan
					if err := p.Object(row, []string{"policy_sources"}, func(field api.JSONField) error {
						if field.Known == 0 {
							opaque = field.Value
						}
						return nil
					}); err != nil {
						return err
					}
					var plan sandboxReceiptRelationPlan
					known := []string{"state_binds", "original_modes", "original_binds", "supplied", "missing", "exposed"}
					if err := p.Object(opaque, known, func(field api.JSONField) error {
						if field.Known >= 0 {
							plan.Groups[field.Known] = field.Value
						}
						return nil
					}); err != nil {
						return err
					}
					return run(p, row, plan, h)
				})
				return err
			})
		})
	})
}

func TestAgentReceiptRelationsKeepGroupsExactAndLookupScoped(t *testing.T) {
	groups := map[string]any{"state_binds": map[string]string{"A": "state"}, "original_modes": map[string]string{"A": "mode"},
		"original_binds": map[string]string{}, "supplied": []string{"A"}, "missing": []string{}, "exposed": []string{"A"}}
	err := agentReceiptRelationsFixture(t, groups, func(p *api.JSONProjection, row api.JSONSpan, plan sandboxReceiptRelationPlan, h *tomlinspect.Inspection) error {
		ctx, err := h.Context()
		if err != nil {
			return err
		}
		values, err := p.Values(plan.Groups[receiptSupplied])
		if err != nil {
			return err
		}
		key, more, err := values.Next()
		if err != nil || !more {
			return tomlinspect.ErrIndexInvariant
		}
		var retained sandboxReceiptRelationLookup
		err = withAgentReceiptRelations(ctx, p, row, plan, h, func(lookup sandboxReceiptRelationLookup) error {
			retained = lookup
			for _, test := range []struct {
				group sandboxReceiptRelationGroup
				value string
			}{{receiptStateBinds, "state"}, {receiptOriginalModes, "mode"}} {
				value, found, err := lookup(test.group, key)
				if err != nil {
					return err
				}
				if !found {
					t.Fatal("an exact key in another physical group was missed")
				}
				equal, err := p.StringEqual(value, test.value)
				if err != nil {
					return err
				}
				if !equal {
					t.Fatal("one group borrowed another group's value")
				}
			}
			_, found, err := lookup(receiptOriginalBinds, key)
			if found {
				t.Fatal("an empty group inherited another group's key")
			}
			return err
		})
		if err != nil {
			return err
		}
		if _, _, err := retained(receiptStateBinds, key); !errors.Is(err, tomlinspect.ErrIndexInvariant) {
			t.Fatalf("a retained lookup escaped its retired phase: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAgentReceiptRelationsIndexUnorderedDenseMapsAndDecodedDuplicates(t *testing.T) {
	var dense strings.Builder
	dense.WriteByte('{')
	for index := 256; index >= 0; index-- {
		if index != 256 {
			dense.WriteByte(',')
		}
		_, _ = fmt.Fprintf(&dense, `"KEY%03d":"value%03d"`, index, index)
	}
	dense.WriteByte('}')
	for _, test := range []struct {
		name  string
		raw   json.RawMessage
		valid bool
	}{{"dense reverse source order", json.RawMessage(dense.String()), true},
		{"decoded duplicate", json.RawMessage(`{"A":"first","\u0041":"second"}`), false}} {
		t.Run(test.name, func(t *testing.T) {
			err := agentReceiptRelationsFixture(t, map[string]any{"state_binds": test.raw}, func(p *api.JSONProjection, row api.JSONSpan, plan sandboxReceiptRelationPlan, h *tomlinspect.Inspection) error {
				ctx, err := h.Context()
				if err != nil {
					return err
				}
				return withAgentReceiptRelations(ctx, p, row, plan, h, func(lookup sandboxReceiptRelationLookup) error {
					fields, err := p.Fields(plan.Groups[receiptStateBinds])
					if err != nil {
						return err
					}
					for {
						field, more, err := fields.Next()
						if err != nil || !more {
							return err
						}
						value, found, err := lookup(receiptStateBinds, field.Name)
						if err != nil {
							return err
						}
						if !found {
							t.Fatal("dense exact lookup missed a physical source key")
						}
						order, err := p.CompareStrings(value, field.Value)
						if err != nil {
							return err
						}
						if order != 0 {
							t.Fatal("dense lookup returned another source value")
						}
					}
				})
			})
			if (err == nil) != test.valid {
				t.Fatalf("dense/decoded-duplicate relation validity=%v; want valid=%v", err, test.valid)
			}
		})
	}
}
