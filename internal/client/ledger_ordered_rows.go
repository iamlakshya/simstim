package client

import (
	"errors"
	"io"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/tomlinspect"
)

func ledgerIndexSpan(span api.JSONSpan) tomlinspect.Span {
	return tomlinspect.Span{Start: span.Start, End: span.End}
}

func ledgerValueSpan(span tomlinspect.Span) api.JSONSpan {
	return api.JSONSpan{Start: span.Start, End: span.End}
}

func ledgerIndexString(value ledgerString) tomlinspect.Span {
	if value.empty {
		return tomlinspect.Span{}
	}
	return ledgerIndexSpan(value.span)
}

// This dedicated phase reuses the existing neutral fixed record. All five
// spans refer to the original row/source; no decoded history is retained.
func ledgerRowRecord(row projectedLedgerRow) tomlinspect.SemanticEntry {
	return tomlinspect.SemanticEntry{
		Kind: uint8(row.rank), Armed: row.removed,
		Name: ledgerIndexSpan(row.span), Sentinel: ledgerIndexString(row.host),
		Identity: ledgerIndexString(row.id), Prefix: ledgerIndexString(row.name),
		Rule: ledgerIndexSpan(row.firstSpan),
	}
}

func ledgerRecordKey(p ledgerValueProjection, record tomlinspect.SemanticEntry) (projectedLedgerRow, error) {
	first, err := readProjectedTime(p, ledgerValueSpan(record.Rule))
	if err != nil {
		return projectedLedgerRow{}, err
	}
	return projectedLedgerRow{
		span: ledgerValueSpan(record.Name),
		host: ledgerString{span: ledgerValueSpan(record.Sentinel), empty: record.Sentinel.End == 0},
		id:   ledgerString{span: ledgerValueSpan(record.Identity), empty: record.Identity.End == 0},
		name: ledgerString{span: ledgerValueSpan(record.Prefix), empty: record.Prefix.End == 0},
		rank: int(record.Kind), removed: record.Armed, first: first,
	}, nil
}

// Every unsorted row is indexed once in the SAME inspection/slab/store. The
// original ordinal breaks exact sort-key ties, preserving stable full-reader
// ordering. Object preflights may overlap this ONE identity tree only.
func (e *ledgerEmission) unsorted(w io.Writer) (err error) {
	if e.inspection == nil {
		return tomlinspect.ErrIndexInvariant
	}
	p := e.ledger.p
	charge, err := e.inspection.CountState(1, ledgerIndexSpan(e.ledger.root), func(counter tomlinspect.StateCounter) error {
		return p.Array(e.ledger.rows, func(api.JSONSpan) error { return counter.Entry() })
	})
	if err != nil {
		return err
	}
	region, err := e.inspection.BeginState(charge)
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
		// Comparisons read fixed records and source scalar cursors only.
		// They never invoke Object, write metadata or enter another tree.
		l, err := ledgerRecordKey(p, left)
		if err != nil {
			return 0, err
		}
		r, err := ledgerRecordKey(p, right)
		if err != nil {
			return 0, err
		}
		order, err := compareProjectedRows(p, l, r)
		if err != nil || order != 0 {
			return order, err
		}
		if a < b {
			return -1, nil
		}
		if a > b {
			return 1, nil
		}
		return 0, nil
	})
	if err != nil {
		return err
	}
	count := 0
	err = p.Array(e.ledger.rows, func(span api.JSONSpan) error {
		row, err := projectLedgerRow(p, span)
		if err != nil {
			return err
		}
		id, err := region.AppendEntry(ledgerRowRecord(row))
		if err != nil {
			return err
		}
		inserted, err := tree.Put(id, 0, id)
		if err != nil {
			return err
		}
		if !inserted {
			return tomlinspect.ErrIndexInvariant
		}
		count++
		return nil
	})
	if err != nil {
		return err
	}
	if count != e.ledger.count {
		return tomlinspect.ErrIndexInvariant
	}
	inserted := e.ledger.found
	err = tree.Walk(func(id, _ uint64, value uint64) error {
		if id != value {
			return tomlinspect.ErrIndexCorrupt
		}
		record, err := region.Entry(id)
		if err != nil {
			return err
		}
		row, err := projectLedgerRow(p, ledgerValueSpan(record.Name))
		if err != nil {
			return err
		}
		if !inserted {
			order, err := compareRowToNewAgent(p, row, e.h, e.hostID, e.now)
			if err != nil {
				return err
			}
			if order > 0 {
				if err := e.newRow(w); err != nil {
					return err
				}
				inserted = true
			}
		}
		return e.row(w, row)
	})
	if err != nil {
		return err
	}
	if !inserted {
		return e.newRow(w)
	}
	return nil
}
