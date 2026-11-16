package client

import (
	"context"
	"io"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

type ledgerString struct {
	span  api.JSONSpan
	empty bool
}
type projectedLedgerRow struct {
	span           api.JSONSpan
	host, id, name ledgerString
	kind           OutboundKind
	rank, count    int
	first, last    projectedTime
	removed        bool
}

func ledgerEmptyString(p *api.JSONProjection, span api.JSONSpan) (ledgerString, error) {
	out := ledgerString{span: span, empty: span.End == 0}
	if out.empty {
		return out, nil
	}
	null, err := p.Literal(span, "null")
	if err != nil {
		return out, err
	}
	if null {
		out.empty = true
		return out, nil
	}
	out.empty, err = p.StringEqual(span, "")
	return out, err
}
func ledgerHostID(p *api.JSONProjection, value ledgerString) error {
	if value.empty {
		return nil
	}
	var text [32]byte
	n := 0
	err := p.WalkString(value.span, func(r rune) error {
		if r > 127 || n == len(text) {
			return badGrantStateError(grantsFileName)
		}
		text[n], n = byte(r), n+1
		return nil
	})
	if err != nil {
		return err
	}
	if !ValidHostID(string(text[:n])) {
		return badGrantStateError(grantsFileName)
	}
	return nil
}
func ledgerCount(p *api.JSONProjection, span api.JSONSpan) (int, error) {
	var n uint64
	maxInt := uint64(^uint(0) >> 1)
	err := p.WalkEncoded(span, func(piece []byte) error {
		for _, b := range piece {
			if b < '0' || b > '9' || n > (maxInt-uint64(b-'0'))/10 {
				return badGrantStateError(grantsFileName)
			}
			n = n*10 + uint64(b-'0')
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, badGrantStateError(grantsFileName)
	}
	return int(n), nil
}
func ledgerKind(p *api.JSONProjection, span api.JSONSpan) (OutboundKind, error) {
	for _, kind := range []OutboundKind{OutboundSecret, OutboundSSHAgent, OutboundSSHKey, OutboundAlertToken, OutboundRepo, OutboundPaste, OutboundPush, OutboundPublicPush, OutboundRootRun} {
		equal, err := p.StringEqual(span, string(kind))
		if err != nil {
			return "", err
		}
		if equal {
			return kind, nil
		}
	}
	return "", badGrantStateError(grantsFileName)
}
func projectLedgerRow(p *api.JSONProjection, span api.JSONSpan) (projectedLedgerRow, error) {
	var row projectedLedgerRow
	row.span = span
	var fields [8]api.JSONSpan
	err := p.Object(span, []string{"host_name", "host_id", "kind", "name", "first_at", "last_at", "count", "removed_at"}, func(field api.JSONField) error {
		if field.Known >= 0 {
			fields[field.Known] = field.Value
		}
		return nil
	})
	if err != nil {
		return row, err
	}
	row.host = ledgerString{span: fields[0]}
	var hostName hostNameValidation
	if err := p.WalkString(fields[0], func(r rune) error { hostName.Rune(r); return nil }); err != nil {
		return row, err
	}
	if !hostName.Valid() {
		return row, badGrantStateError(grantsFileName)
	}
	row.id, err = ledgerEmptyString(p, fields[1])
	if err != nil {
		return row, err
	}
	if err := ledgerHostID(p, row.id); err != nil {
		return row, err
	}
	row.kind, err = ledgerKind(p, fields[2])
	if err != nil {
		return row, err
	}
	row.rank = grantKindRank(row.kind)
	row.name, err = ledgerEmptyString(p, fields[3])
	if err != nil {
		return row, err
	}
	if err := projectedLedgerName(p, row.kind, fields[3]); err != nil {
		return row, err
	}
	row.first, err = readProjectedTime(p, fields[4])
	if err != nil {
		return row, err
	}
	row.last, err = readProjectedTime(p, fields[5])
	if err != nil {
		return row, err
	}
	if row.first.instant.IsZero() || row.last.instant.IsZero() || row.last.instant.Before(row.first.instant) || !row.first.marshalable() || !row.last.marshalable() {
		return row, badGrantStateError(grantsFileName)
	}
	row.count, err = ledgerCount(p, fields[6])
	if err != nil {
		return row, err
	}
	if fields[7].End != 0 {
		null, err := p.Literal(fields[7], "null")
		if err != nil {
			return row, err
		}
		if !null {
			removed, err := readProjectedTime(p, fields[7])
			if err != nil {
				return row, err
			}
			if removed.instant.IsZero() || !removed.marshalable() {
				return row, badGrantStateError(grantsFileName)
			}
			row.removed = true
		}
	}
	return row, nil
}
func compareLedgerString(p *api.JSONProjection, a, b ledgerString) (int, error) {
	if a.empty {
		if b.empty {
			return 0, nil
		}
		return -1, nil
	}
	if b.empty {
		return 1, nil
	}
	return p.CompareStrings(a.span, b.span)
}
func compareProjectedRows(p *api.JSONProjection, a, b projectedLedgerRow) (int, error) {
	for _, pair := range [][2]ledgerString{{a.host, b.host}, {a.id, b.id}} {
		order, err := compareLedgerString(p, pair[0], pair[1])
		if err != nil || order != 0 {
			return order, err
		}
	}
	if a.rank < b.rank {
		return -1, nil
	}
	if a.rank > b.rank {
		return 1, nil
	}
	order, err := compareLedgerString(p, a.name, b.name)
	if err != nil || order != 0 {
		return order, err
	}
	if a.removed != b.removed {
		if !a.removed {
			return -1, nil
		}
		return 1, nil
	}
	return a.first.instant.Compare(b.first.instant), nil
}

// compareLedgerText compares an encoded field to a bounded trusted host/ID.
// It never materialises an unrelated record's string.
func compareLedgerText(p *api.JSONProjection, value ledgerString, want string) (int, error) {
	if value.empty {
		if want == "" {
			return 0, nil
		}
		return -1, nil
	}
	at, order := 0, 0
	err := p.WalkString(value.span, func(r rune) error {
		if order != 0 {
			return nil
		}
		if at == len(want) {
			order = 1
			return nil
		}
		expected := rune(want[at])
		at++
		if r < expected {
			order = -1
		} else if r > expected {
			order = 1
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if order == 0 && at < len(want) {
		order = -1
	}
	return order, nil
}
func compareRowToNewAgent(p *api.JSONProjection, row projectedLedgerRow, h Host, hostID string, first time.Time) (int, error) {
	order, err := compareLedgerText(p, row.host, h.Name)
	if err != nil || order != 0 {
		return order, err
	}
	order, err = compareLedgerText(p, row.id, hostID)
	if err != nil || order != 0 {
		return order, err
	}
	rank := grantKindRank(OutboundSSHAgent)
	if row.rank < rank {
		return -1, nil
	}
	if row.rank > rank {
		return 1, nil
	}
	if !row.name.empty {
		return 1, nil
	}
	if row.removed {
		return 1, nil
	}
	return row.first.instant.Compare(first), nil
}
func ledgerRowMatchesAgent(p *api.JSONProjection, row projectedLedgerRow, h Host, hostID string) (bool, error) {
	if row.kind != OutboundSSHAgent || row.removed || !row.name.empty {
		return false, nil
	}
	order, err := compareLedgerText(p, row.host, h.Name)
	if err != nil || order != 0 {
		return false, err
	}
	order, err = compareLedgerText(p, row.id, hostID)
	return err == nil && order == 0, err
}

type projectedLedger struct {
	p             *api.JSONProjection
	root, rows    api.JSONSpan
	selected      projectedLedgerRow
	found, sorted bool
	count         int
}

func projectLedger(ctx context.Context, source io.ReaderAt, size int64, h Host, hostID string) (*projectedLedger, error) {
	p, err := api.NewJSONProjection(ctx, source, size)
	if err != nil {
		return nil, err
	}
	root, err := p.ObjectRoot()
	if err != nil {
		return nil, err
	}
	var fields [2]api.JSONSpan
	if err := p.Object(root, []string{"version", "grants"}, func(field api.JSONField) error {
		if field.Known >= 0 {
			fields[field.Known] = field.Value
		}
		return nil
	}); err != nil {
		return nil, err
	}
	version, err := p.Literal(fields[0], "1")
	if err != nil {
		return nil, err
	}
	if !version {
		return nil, badGrantStateError(grantsFileName)
	}
	result := &projectedLedger{p: p, root: root, rows: fields[1], sorted: true}
	var previous projectedLedgerRow
	err = p.Array(fields[1], func(span api.JSONSpan) error {
		row, err := projectLedgerRow(p, span)
		if err != nil {
			return err
		}
		if result.count > 0 {
			order, err := compareProjectedRows(p, previous, row)
			if err != nil {
				return err
			}
			if order > 0 {
				result.sorted = false
			}
		}
		if !result.found {
			match, err := ledgerRowMatchesAgent(p, row, h, hostID)
			if err != nil {
				return err
			}
			if match {
				result.selected, result.found = row, true
				if row.count == int(^uint(0)>>1) {
					return grantCountError()
				}
			}
		}
		if result.count == int(^uint(0)>>1) {
			return badGrantStateError(grantsFileName)
		}
		result.count++
		previous = row
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
