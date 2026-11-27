package client

import (
	"context"
	"errors"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/tomlinspect"
)

// The ordinary reader sorts and compacts original non-lineage names. The
// receipt is already strictly ordered. Index its source spans once and stream
// the original names without imposing receipt order on historical Secrets.
func checkAgentReceiptNames(ctx context.Context, p *api.JSONProjection, row, original, receipt api.JSONSpan, h *tomlinspect.Inspection) (err error) {
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
	for _, span := range [...]api.JSONSpan{original, receipt} {
		if span.End != 0 && (span.Start < row.Start || span.End > row.End || span.End <= span.Start) {
			return tomlinspect.ErrIndexInvariant
		}
	}
	// This temporary region counts only actual receipt-name elements. Its
	// existing +1 candidate is reused for every original-name lookup. This
	// region never overlaps the semantic state or receipt-relation region;
	// the actual receipt adapter calls it after state validation returns.
	charge, err := h.CountState(1, tomlinspect.Span{Start: row.Start, End: row.End}, func(counter tomlinspect.StateCounter) error {
		return walkAgentReceiptNames(p, receipt, func(api.JSONSpan) error { return counter.Entry() })
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
		return p.CompareStrings(api.JSONSpan{Start: left.Name.Start, End: left.Name.End}, api.JSONSpan{Start: right.Name.Start, End: right.Name.End})
	})
	if err != nil {
		return err
	}
	var count uint64
	err = walkAgentReceiptNames(p, receipt, func(name api.JSONSpan) error {
		id, err := region.AppendEntry(tomlinspect.SemanticEntry{Name: tomlinspect.Span{Start: name.Start, End: name.End}})
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
		// Successful append IDs are checked uint32 values, so count cannot
		// overflow uint64. The receipt cursor also enforces strict ordering.
		count++
		return nil
	})
	if err != nil {
		return err
	}
	query, err := region.AppendEntry(tomlinspect.SemanticEntry{})
	if err != nil {
		return err
	}
	var seen uint64
	var names api.JSONValues
	if original.End != 0 {
		null, failure := p.Literal(original, "null")
		if failure != nil {
			return failure
		}
		if null {
			original = api.JSONSpan{}
		} else {
			names, err = p.Values(original)
			if err != nil {
				return err
			}
		}
	}
	for original.End != 0 {
		name, more, err := names.Next()
		if err != nil {
			return err
		}
		if !more {
			break
		}
		if err := region.UpdateEntry(query, tomlinspect.SemanticEntry{Name: tomlinspect.Span{Start: name.Start, End: name.End}}); err != nil {
			return err
		}
		matched, found, err := tree.Find(query, 0)
		if err != nil {
			return err
		}
		if !found {
			return sandboxReceiptError()
		}
		entry, err := region.Entry(matched)
		if err != nil {
			return err
		}
		if !entry.Armed {
			entry.Armed = true
			if err := region.UpdateEntry(matched, entry); err != nil {
				return err
			}
			seen++ // Each durable entry is marked at most once: seen <= count.
		}
	}
	if seen != count {
		return sandboxReceiptError()
	}
	return ctx.Err()
}

func walkAgentReceiptNames(p *api.JSONProjection, span api.JSONSpan, visit func(api.JSONSpan) error) error {
	names, err := p.OrderedStrings(span)
	if err != nil {
		return err
	}
	for {
		name, more, err := names.Next()
		if err != nil || !more {
			return err
		}
		if err := visit(name); err != nil {
			return err
		}
	}
}
