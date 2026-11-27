package client

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
)

// Called ONLY by the canonical reserveSandbox byte callback. It consumes one
// private intent already approved by Main outside membership, then repeats
// exact local parent/root/CAS facts. It never asks/fetches or re-enters a caller.
func validateFreshLineageReservationContext(ctx context.Context, file *sandboxFile, child *SandboxPolicy) error {
	permit, _ := ctx.Value(freshSandboxReservationKey{}).(*freshSandboxReservationPermit)
	if permit == nil || !permit.active.Load() || permit.intent.child == nil || !reflect.DeepEqual(permit.intent.child, child) ||
		!permit.used.CompareAndSwap(false, true) {
		return joinIntentError()
	}
	s := permit.intent.snapshot
	c := permit.intent.conn
	if s == nil || c == nil || s.conn != c || file.schema < s.schema || file.schema < 2 || file.schema > 4 ||
		!bytes.Equal(file.sessions[s.parent.SessionID], s.parentRaw) || !bytes.Equal(file.sessions[s.root.SessionID], s.rootRaw) {
		return joinIntentError()
	}
	if err := c.hostTransport.checkConn(c); err != nil {
		return err
	}
	parent, err := decodeSandboxContext(ctx, s.parent.SessionID, file.sessions[s.parent.SessionID], file.schema)
	if err != nil {
		return err
	}
	root, err := decodeSandboxContext(ctx, s.root.SessionID, file.sessions[s.root.SessionID], file.schema)
	if err != nil {
		return err
	}
	if err := validateFreshSandboxAncestors(&parent, &root, c.HostID()); err != nil {
		return err
	}
	if !sameSandboxCreation(&parent, s.parent) || !sameSandboxCreation(&root, s.root) || parent.Ceiling != s.parent.Ceiling || root.Ceiling != s.root.Ceiling ||
		child.Ceiling > parent.Ceiling || child.Ceiling > root.Ceiling {
		return joinIntentError()
	}
	if err := validateFreshSandboxCohort(ctx, &permit.intent, permit.cohort); err != nil {
		return err
	}
	if err := checkSandboxLineageSchemaPromotion(ctx, file); err != nil {
		return err
	}
	// The monotonic barrier and prospective child publish in this SAME file
	// replacement; no parent/root row is rewritten or given the child's names.
	file.schema, file.root["schema"] = 4, json.RawMessage("4")
	return ctx.Err()
}

// Coarse schema promotion must not turn old opaque v2/future rows into fresh
// local authority. Preserve those original bytes and refuse the promotion.
func checkSandboxLineageSchemaPromotion(ctx context.Context, file *sandboxFile) error {
	if file.schema == 4 {
		return ctx.Err()
	}
	for id, raw := range file.sessions {
		if err := ctx.Err(); err != nil {
			return err
		}
		row, err := decodeSandboxContext(ctx, id, raw, file.schema)
		if err != nil {
			return err
		}
		if row.FutureLineage {
			return futureLineageError()
		}
	}
	return nil
}
