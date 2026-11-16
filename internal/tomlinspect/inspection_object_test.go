//go:build darwin || linux

package tomlinspect

import (
	"errors"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

type objectSourceComparer struct{ p *api.JSONProjection }

func (c objectSourceComparer) CompareObjectNames(a, b Span) (int, error) {
	return c.p.CompareStrings(jsonObjectSpan(a), jsonObjectSpan(b))
}

func jsonObjectSpan(span Span) api.JSONSpan { return api.JSONSpan{Start: span.Start, End: span.End} }

func countObjectFixture(h *Inspection, p *api.JSONProjection, span Span) (ObjectCharge, error) {
	return h.CountObject(1, span, func(counter ObjectCounter) error {
		fields, err := p.Fields(jsonObjectSpan(span))
		if err != nil {
			return err
		}
		for {
			_, more, err := fields.Next()
			if err != nil || !more {
				return err
			}
			if err := counter.Field(); err != nil {
				return err
			}
		}
	})
}

func TestObjectPreflightOwnsActualFieldsAndRetiresBeforeVisitor(t *testing.T) {
	inspector, factory, body := completeScopeFixture(t)
	var retained ObjectRegion
	err := inspector.WithSources(t.Context(), factory.sources[:], func(h *Inspection) error {
		if err := h.InspectTOML(0, syntaxOnlyVisitor{}); err != nil {
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
			return ErrIndexInvariant
		}
		span := Span{End: int64(len(body))}
		for range 2 {
			charge, err := countObjectFixture(h, p, span)
			if err != nil {
				return err
			}
			object, err := h.BeginObject(charge, objectSourceComparer{p: p})
			if err != nil {
				return err
			}
			fields, err := p.Fields(jsonObjectSpan(span))
			if err != nil {
				return err
			}
			for {
				field, more, err := fields.Next()
				if err != nil {
					return err
				}
				if !more {
					break
				}
				inserted, err := object.PutName(Span{Start: field.Name.Start, End: field.Name.End})
				if err != nil {
					return err
				}
				if !inserted {
					t.Fatal("distinct physical object fields collapsed")
				}
			}
			retained = object
			if err := object.FinishObject(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := retained.PutName(Span{Start: 1, End: 2}); !errors.Is(err, ErrIndexInvariant) {
		t.Fatalf("an object handle adopted a later region: %v", err)
	}
	t.Logf("extra_fixed_object_header_bytes=%d", ObjectPreflightHeaderBytes)
}

func TestObjectPreflightPermitsOnlyClosedReceiptRelationOverlap(t *testing.T) {
	for _, semantic := range []bool{false, true} {
		name := "receipt relation"
		if semantic {
			name = "semantic tree"
		}
		t.Run(name, func(t *testing.T) {
			inspector, factory, body := completeScopeFixture(t)
			err := inspector.WithSources(t.Context(), factory.sources[:], func(h *Inspection) error {
				if err := h.InspectTOML(0, syntaxOnlyVisitor{}); err != nil {
					return err
				}
				span := Span{End: int64(len(body))}
				stateCharge, err := h.CountState(1, span, func(c StateCounter) error { return c.Entry() })
				if err != nil {
					return err
				}
				state, err := h.BeginState(stateCharge)
				if err != nil {
					return err
				}
				kind := RelationIdentity
				if semantic {
					kind = RelationSentinel
				}
				if _, err := state.NewTree(kind, func(a, _ uint64, b, _ uint64) (int, error) {
					if a < b {
						return -1, nil
					}
					if a > b {
						return 1, nil
					}
					return 0, nil
				}); err != nil {
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
				if err != nil {
					return err
				}
				charge, err := countObjectFixture(h, p, span)
				if err != nil {
					return err
				}
				object, err := h.BeginObject(charge, objectSourceComparer{p: p})
				if err != nil {
					return err
				}
				// Resource close does not mint a complete-pass fact. This
				// overlap test grants no parser or authorization result.
				if err := object.Close(); err != nil {
					return err
				}
				return state.Close()
			})
			if semantic && !errors.Is(err, ErrIndexInvariant) || !semantic && err != nil {
				t.Fatalf("semantic overlap=%v: %v", semantic, err)
			}
		})
	}
}
