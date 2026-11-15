package client

import (
	"context"
	"errors"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/tomlinspect"
)

// Receipt predicates own their grammar; this callback supplies only an exact
// source index while one temporary region is live on the actual inspection.
func withAgentReceiptRelations(ctx context.Context, p *api.JSONProjection, row api.JSONSpan, plan sandboxReceiptRelationPlan, h *tomlinspect.Inspection, check func(sandboxReceiptRelationLookup) error) (err error) {
	if check == nil {
		return tomlinspect.ErrIndexInvariant
	}
	active, err := h.Context()
	if err != nil {
		return err
	}
	source, err := h.Source(1)
	if err != nil {
		return err
	}
	if !p.MatchesSource(active, source, source.Size()) || !p.MatchesSource(ctx, source, source.Size()) {
		return tomlinspect.ErrIndexInvariant
	}
	if err := h.CheckSpan(1, tomlinspect.Span{Start: row.Start, End: row.End}); err != nil {
		return err
	}
	for _, span := range plan.Groups {
		if span.End != 0 && (span.Start < row.Start || span.End > row.End || span.End <= span.Start) {
			return tomlinspect.ErrIndexInvariant
		}
	}
	charge, err := h.CountState(1, tomlinspect.Span{Start: row.Start, End: row.End}, func(counter tomlinspect.StateCounter) error {
		return walkAgentReceiptRelations(p, plan, func(sandboxReceiptRelationGroup, api.JSONSpan, api.JSONSpan) error {
			return counter.Entry()
		})
	})
	if err != nil {
		return err
	}
	region, err := h.BeginState(charge)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, region.Close()) }()
	tree, err := region.NewTree(tomlinspect.RelationIdentity, func(a, _ uint64, b, _ uint64) (int, error) {
		left, err := region.Entry(a)
		if err != nil {
			return 0, err
		}
		right, err := region.Entry(b)
		if err != nil {
			return 0, err
		}
		if left.Kind < right.Kind {
			return -1, nil
		}
		if left.Kind > right.Kind {
			return 1, nil
		}
		return p.CompareStrings(api.JSONSpan{Start: left.Name.Start, End: left.Name.End}, api.JSONSpan{Start: right.Name.Start, End: right.Name.End})
	})
	if err != nil {
		return err
	}
	err = walkAgentReceiptRelations(p, plan, func(group sandboxReceiptRelationGroup, key, value api.JSONSpan) error {
		entry := tomlinspect.SemanticEntry{
			Kind: uint8(group) + 1,
			Name: tomlinspect.Span{Start: key.Start, End: key.End},
			// In this dedicated phase the second neutral span refers to
			// the source value. No decoded sentinel/value bytes are stored.
			Sentinel: tomlinspect.Span{Start: value.Start, End: value.End},
		}
		id, err := region.AppendEntry(entry)
		if err != nil {
			return err
		}
		inserted, err := tree.Put(id, 0, id)
		if err != nil {
			return err
		}
		if !inserted {
			return sandboxReceiptError()
		}
		return nil
	})
	if err != nil {
		return err
	}
	query, err := region.AppendEntry(tomlinspect.SemanticEntry{})
	if err != nil {
		return err
	}
	live := true
	defer func() { live = false }()
	lookup := func(group sandboxReceiptRelationGroup, key api.JSONSpan) (api.JSONSpan, bool, error) {
		if !live || uint64(group) >= uint64(len(plan.Groups)) || key.Start < row.Start || key.End > row.End || key.End <= key.Start {
			return api.JSONSpan{}, false, tomlinspect.ErrIndexInvariant
		}
		// This one candidate is NEVER a durable tree key. Every indexed
		// key and value source span stays immutable until region retirement.
		if err := region.UpdateEntry(query, tomlinspect.SemanticEntry{Kind: uint8(group) + 1, Name: tomlinspect.Span{Start: key.Start, End: key.End}}); err != nil {
			return api.JSONSpan{}, false, err
		}
		id, found, err := tree.Find(query, 0)
		if err != nil || !found {
			return api.JSONSpan{}, found, err
		}
		entry, err := region.Entry(id)
		if err != nil {
			return api.JSONSpan{}, false, err
		}
		return api.JSONSpan{Start: entry.Sentinel.Start, End: entry.Sentinel.End}, true, nil
	}
	if err := check(lookup); err != nil {
		return err
	}
	return ctx.Err()
}

func walkAgentReceiptRelations(p *api.JSONProjection, plan sandboxReceiptRelationPlan, visit func(sandboxReceiptRelationGroup, api.JSONSpan, api.JSONSpan) error) error {
	for index, span := range plan.Groups {
		if span.End == 0 {
			continue
		}
		null, err := p.Literal(span, "null")
		if err != nil {
			return err
		}
		if null {
			continue
		}
		group := sandboxReceiptRelationGroup(index)
		if group <= receiptOriginalBinds {
			fields, err := p.Fields(span)
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
				if err := visit(group, field.Name, field.Value); err != nil {
					return err
				}
			}
			continue
		}
		values, err := p.Values(span)
		if err != nil {
			return err
		}
		for {
			value, more, err := values.Next()
			if err != nil {
				return err
			}
			if !more {
				break
			}
			if err := visit(group, value, value); err != nil {
				return err
			}
		}
	}
	return nil
}
