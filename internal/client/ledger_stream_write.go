package client

import (
	"context"
	"encoding/json"
	"io"
	"strconv"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
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
func copyLedgerSpan(p *api.JSONProjection, w io.Writer, span api.JSONSpan) error {
	return p.WalkEncoded(span, func(piece []byte) error { return writeLedgerBytes(w, piece) })
}
func writeLedgerComma(w io.Writer, first *bool) error {
	if *first {
		*first = false
		return nil
	}
	return writeLedgerBytes(w, []byte(","))
}
func writeUpdatedAgentRow(p *api.JSONProjection, w io.Writer, row projectedLedgerRow, now time.Time) error {
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
	if err := writeLedgerBytes(w, []byte("{")); err != nil {
		return err
	}
	first := true
	err = p.Object(row.span, []string{"first_at", "last_at", "count"}, func(field api.JSONField) error {
		if err := writeLedgerComma(w, &first); err != nil {
			return err
		}
		if err := copyLedgerSpan(p, w, field.Name); err != nil {
			return err
		}
		if err := writeLedgerBytes(w, []byte(":")); err != nil {
			return err
		}
		switch field.Known {
		case 0:
			return writeLedgerBytes(w, firstJSON)
		case 1:
			return writeLedgerBytes(w, lastJSON)
		case 2:
			return writeLedgerBytes(w, count)
		default:
			return copyLedgerSpan(p, w, field.Value)
		}
	})
	if err != nil {
		return err
	}
	return writeLedgerBytes(w, []byte("}"))
}

type ledgerEmission struct {
	ledger  *projectedLedger
	h       Host
	hostID  string
	now     time.Time
	newJSON []byte
	first   bool
}

func (e *ledgerEmission) row(w io.Writer, row projectedLedgerRow) error {
	if err := writeLedgerComma(w, &e.first); err != nil {
		return err
	}
	if e.ledger.found && row.span == e.ledger.selected.span {
		return writeUpdatedAgentRow(e.ledger.p, w, row, e.now)
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

type ledgerOrderedRow struct {
	row     projectedLedgerRow
	ordinal int
	fresh   bool
}

func (e *ledgerEmission) compare(a, b ledgerOrderedRow) (int, error) {
	if a.fresh {
		if b.fresh {
			return 0, nil
		}
		order, err := compareRowToNewAgent(e.ledger.p, b.row, e.h, e.hostID, e.now)
		return -order, err
	}
	if b.fresh {
		return compareRowToNewAgent(e.ledger.p, a.row, e.h, e.hostID, e.now)
	}
	order, err := compareProjectedRows(e.ledger.p, a.row, b.row)
	if err != nil || order != 0 {
		return order, err
	}
	if a.ordinal < b.ordinal {
		return -1, nil
	}
	if a.ordinal > b.ordinal {
		return 1, nil
	}
	return 0, nil
}

// Unsorted legacy files use exact offset rescans under the same snapshot.
// This is intentionally cancellation-bound, with worst-case CPU unproven.
func (e *ledgerEmission) unsorted(w io.Writer) error {
	total := e.ledger.count
	if !e.ledger.found {
		if total == int(^uint(0)>>1) {
			return badGrantStateError(grantsFileName)
		}
		total++
	}
	var previous ledgerOrderedRow
	havePrevious := false
	for range total {
		var best ledgerOrderedRow
		haveBest := false
		choose := func(candidate ledgerOrderedRow) error {
			if havePrevious {
				order, err := e.compare(candidate, previous)
				if err != nil {
					return err
				}
				if order <= 0 {
					return nil
				}
			}
			if haveBest {
				order, err := e.compare(candidate, best)
				if err != nil {
					return err
				}
				if order >= 0 {
					return nil
				}
			}
			best, haveBest = candidate, true
			return nil
		}
		ordinal := 0
		err := e.ledger.p.Array(e.ledger.rows, func(span api.JSONSpan) error {
			row, err := projectLedgerRow(e.ledger.p, span)
			if err != nil {
				return err
			}
			candidate := ledgerOrderedRow{row: row, ordinal: ordinal}
			ordinal++
			return choose(candidate)
		})
		if err != nil {
			return err
		}
		if !e.ledger.found {
			if err := choose(ledgerOrderedRow{fresh: true, ordinal: e.ledger.count}); err != nil {
				return err
			}
		}
		if !haveBest {
			return badGrantStateError(grantsFileName)
		}
		if best.fresh {
			if err := e.newRow(w); err != nil {
				return err
			}
		} else if err := e.row(w, best.row); err != nil {
			return err
		}
		previous, havePrevious = best, true
	}
	return nil
}
func (e *ledgerEmission) write(w io.Writer) error {
	p := e.ledger.p
	if err := writeLedgerBytes(w, []byte("{")); err != nil {
		return err
	}
	firstField := true
	err := p.Object(e.ledger.root, []string{"grants"}, func(field api.JSONField) error {
		if err := writeLedgerComma(w, &firstField); err != nil {
			return err
		}
		if err := copyLedgerSpan(p, w, field.Name); err != nil {
			return err
		}
		if err := writeLedgerBytes(w, []byte(":")); err != nil {
			return err
		}
		if field.Known < 0 {
			return copyLedgerSpan(p, w, field.Value)
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
		return writeLedgerBytes(w, []byte("]"))
	})
	if err != nil {
		return err
	}
	return writeLedgerBytes(w, []byte("}\n"))
}

func recordAgentGrantFD(ctx context.Context, h Host, hostID string) error {
	return withStateFileFD(ctx, nil, grantsFileName, true, func(view stateFileView) (stateFileReplace, error) {
		if !view.Exists {
			now := nowUTC()
			fresh := grantsFile{Version: 1, Grants: []GrantRecord{{HostName: h.Name, HostID: hostID, Kind: OutboundSSHAgent, FirstAt: now, LastAt: now, Count: 1}}}
			data, err := encodeGrantsFile(fresh)
			if err != nil {
				return nil, err
			}
			return func(_ context.Context, w io.Writer) error { return writeLedgerBytes(w, data) }, nil
		}
		ledger, err := projectLedger(ctx, view.ReaderAt, view.Size, h, hostID)
		if err != nil {
			return nil, agentLedgerProjectionError(ctx, err)
		}
		now := nowUTC()
		emission := &ledgerEmission{ledger: ledger, h: h, hostID: hostID, now: now}
		if !ledger.found {
			emission.newJSON, err = json.Marshal(GrantRecord{HostName: h.Name, HostID: hostID, Kind: OutboundSSHAgent, FirstAt: now, LastAt: now, Count: 1})
			if err != nil {
				return nil, err
			}
		}
		return func(_ context.Context, w io.Writer) error {
			if err := emission.write(w); err != nil {
				return agentLedgerProjectionError(ctx, err)
			}
			return nil
		}, nil
	})
}
func agentLedgerProjectionError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return wrapGrantStateError(grantsFileName, err)
}
