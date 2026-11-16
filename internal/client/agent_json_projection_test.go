//go:build darwin || linux

package client

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/tomlinspect"
)

func TestGuardedObjectMatchesCanonicalAliasesDuplicatesAndOpaqueBodies(t *testing.T) {
	var dense strings.Builder
	dense.WriteByte('{')
	for index := 256; index >= 0; index-- {
		if index != 256 {
			dense.WriteByte(',')
		}
		_, _ = fmt.Fprintf(&dense, `"K%03d":%d`, index, index)
	}
	dense.WriteByte('}')
	for _, test := range []struct {
		name string
		raw  json.RawMessage
	}{
		{"reverse unknown fields", json.RawMessage(dense.String())},
		{"decoded duplicate", json.RawMessage(`{"same":1,"\u0073ame":2}`)},
		{"case alias", json.RawMessage(`{"Version":1}`)},
		{"unicode alias", json.RawMessage(`{"ver\u017fion":1}`)},
		{"opaque nested duplicates", json.RawMessage(`{"future":{"duplicate":1,"duplicate":2},"version":1}`)},
		{"empty unknown name", json.RawMessage(`{"":0}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, reference := api.ProjectedJSONObjectFields(t.Context(), test.raw, "version")
			err := agentReceiptRelationsFixture(t, map[string]any{"state_binds": test.raw}, func(p *api.JSONProjection, _ api.JSONSpan, plan sandboxReceiptRelationPlan, h *tomlinspect.Inspection) error {
				guarded, err := newAgentJSONProjection(p, h)
				if err != nil {
					return err
				}
				return guarded.Object(plan.Groups[receiptStateBinds], []string{"version"}, func(field api.JSONField) error {
					if field.Known >= 0 {
						equal, err := p.Literal(field.Value, "1")
						if err != nil {
							return err
						}
						if !equal {
							t.Fatal("a known field lost its canonical classifier index")
						}
					}
					return nil
				})
			})
			if (err == nil) != (reference == nil) {
				t.Fatalf("guarded/canonical object validity differs: guarded=%v reference=%v", err, reference)
			}
		})
	}
}

func TestGuardedObjectCanRunInsideActualReceiptRelationPhase(t *testing.T) {
	var binding strings.Builder
	binding.WriteByte('{')
	for index := 128; index >= 0; index-- {
		if index != 128 {
			binding.WriteByte(',')
		}
		_, _ = fmt.Fprintf(&binding, `"future%03d":%d`, index, index)
	}
	binding.WriteByte('}')
	groups := map[string]any{"original_binds": map[string]json.RawMessage{"KEY": json.RawMessage(binding.String())}, "supplied": []string{"KEY"}}
	err := agentReceiptRelationsFixture(t, groups, func(p *api.JSONProjection, row api.JSONSpan, plan sandboxReceiptRelationPlan, h *tomlinspect.Inspection) error {
		guarded, err := newAgentJSONProjection(p, h)
		if err != nil {
			return err
		}
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
		return withAgentReceiptRelations(ctx, p, row, plan, h, func(lookup sandboxReceiptRelationLookup) error {
			value, found, err := lookup(receiptOriginalBinds, key)
			if err != nil {
				return err
			}
			if !found {
				t.Fatal("actual relation index missed the source object")
			}
			seen := 0
			if err := guarded.Object(value, []string{"hosts", "header", "format"}, func(api.JSONField) error { seen++; return nil }); err != nil {
				return err
			}
			if seen != 129 {
				t.Fatalf("unknown binding metadata changed object visitation: %d", seen)
			}
			// The object index already retired; the parent exact relation
			// remains live and still names the identical source span.
			again, found, err := lookup(receiptOriginalBinds, key)
			if err != nil {
				return err
			}
			if !found || again != value {
				t.Fatal("object retirement changed its parent relation")
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}
