package client

import (
	"context"
	"io"
	"strconv"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/tomlinspect"
)

func writeLedgerBytes(w io.Writer, value []byte) error {
	n, err := w.Write(value)
	if err != nil {
		return err
	}
	if n != len(value) {
		return io.ErrShortWrite
	}
	return nil
}
func copyLedgerSpan(p ledgerValueProjection, w io.Writer, span api.JSONSpan) error {
	return p.WalkEncoded(span, func(piece []byte) error { return writeLedgerBytes(w, piece) })
}
func writeLedgerComma(w io.Writer, first *bool) error {
	if *first {
		*first = false
		return nil
	}
	return writeLedgerBytes(w, []byte(","))
}
func writeUpdatedAgentRow(p ledgerValueProjection, w io.Writer, row projectedLedgerRow, now time.Time) error {
	firstJSON, err := row.first.marshalJSON()
	if err != nil {
		return err
	}
	last := row.last
	if now.After(last.instant) {
		last = projectedTime{instant: now, local: now}
	}
	lastJSON, err := last.marshalJSON()
	if err != nil {
		return err
	}
	var countBuffer [20]byte
	count := strconv.AppendInt(countBuffer[:0], int64(row.count+1), 10)
	changes := [3]ledgerValueReplacement{{row.firstSpan, firstJSON}, {row.lastSpan, lastJSON}, {row.countSpan, count}}
	// These are the three actual value spans from the complete semantic
	// projection, not field-name guesses or a second parse of opaque bytes.
	for i := 1; i < len(changes); i++ {
		for j := i; j > 0 && changes[j].span.Start < changes[j-1].span.Start; j-- {
			changes[j], changes[j-1] = changes[j-1], changes[j]
		}
	}
	at := row.span.Start
	for _, change := range changes {
		if change.span.Start < at || change.span.End <= change.span.Start || change.span.End > row.span.End {
			return tomlinspect.ErrIndexInvariant
		}
		if err := copyLedgerSpan(p, w, api.JSONSpan{Start: at, End: change.span.Start}); err != nil {
			return err
		}
		if err := writeLedgerBytes(w, change.value); err != nil {
			return err
		}
		at = change.span.End
	}
	return copyLedgerSpan(p, w, api.JSONSpan{Start: at, End: row.span.End})
}

type ledgerValueReplacement struct {
	span  api.JSONSpan
	value []byte
}

type ledgerEmission struct {
	ledger     *projectedLedger
	completed  *ledgerEmissionSource
	inspection *tomlinspect.Inspection
	h          Host
	hostID     string
	now        time.Time
	newJSON    []byte
	first      bool
}

func (e *ledgerEmission) row(w io.Writer, row projectedLedgerRow) error {
	if err := writeLedgerComma(w, &e.first); err != nil {
		return err
	}
	if e.ledger.found && row.span == e.ledger.selected.span {
		return writeUpdatedAgentRow(e.completed, w, e.ledger.selected, e.now)
	}
	return copyLedgerSpan(e.ledger.p, w, row.span)
}
func (e *ledgerEmission) newRow(w io.Writer) error {
	if err := writeLedgerComma(w, &e.first); err != nil {
		return err
	}
	return writeLedgerBytes(w, e.newJSON)
}
func (e *ledgerEmission) sorted(w io.Writer) error {
	inserted := e.ledger.found
	err := e.ledger.p.Array(e.ledger.rows, func(span api.JSONSpan) error {
		if e.ledger.found {
			// The completed full pass already established stable order and
			// the exact selected row. Other rows are copied as opaque spans.
			return e.row(w, projectedLedgerRow{span: span})
		}
		row, err := projectLedgerRow(e.ledger.p, span)
		if err != nil {
			return err
		}
		if !inserted {
			order, err := compareRowToNewAgent(e.ledger.p, row, e.h, e.hostID, e.now)
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

func (e *ledgerEmission) write(w io.Writer) error {
	if e.completed == nil {
		return tomlinspect.ErrIndexInvariant
	}
	if err := e.completed.check(); err != nil {
		return err
	}
	p := e.completed
	root, rows := e.ledger.root, e.ledger.rows
	if rows.Start <= root.Start || rows.End <= rows.Start || rows.End >= root.End {
		return tomlinspect.ErrIndexInvariant
	}
	// The complete root pass already located and validated its ONE array.
	// Splice that exact value. Original whitespace/opaque metadata stay byte
	// identical and are copied by the existing bounded output byte walk.
	if err := copyLedgerSpan(p, w, api.JSONSpan{Start: root.Start, End: rows.Start}); err != nil {
		return err
	}
	if err := writeLedgerBytes(w, []byte("[")); err != nil {
		return err
	}
	e.first = true
	if e.ledger.sorted {
		if err := e.sorted(w); err != nil {
			return err
		}
	} else if err := e.unsorted(w); err != nil {
		return err
	}
	if err := writeLedgerBytes(w, []byte("]")); err != nil {
		return err
	}
	if err := copyLedgerSpan(p, w, api.JSONSpan{Start: rows.End, End: root.End}); err != nil {
		return err
	}
	if err := e.completed.check(); err != nil {
		return err
	}
	return writeLedgerBytes(w, []byte("\n"))
}

func agentLedgerProjectionError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return wrapGrantStateError(grantsFileName, err)
}
