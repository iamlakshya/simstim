package client

import (
	"bytes"
	"context"
	"encoding/json"
)

// This is one original local file/Source snapshot, not selection or cohort
// authority. Only the capture helper constructs it before any fresh fetch.
type freshSandboxReservationSnapshot struct {
	conn               *Conn
	host               Host
	source             hostStateSourceWitness
	parent, root       *SandboxPolicy
	parentRaw, rootRaw json.RawMessage
	schema             int
}

func captureFreshSandboxReservationContext(ctx context.Context, c *Conn, parentID string) (*freshSandboxReservationSnapshot, error) {
	if c == nil {
		return nil, joinIntentError()
	}
	if err := checkSessionID(c.Host.Name, parentID); err != nil {
		return nil, err
	}
	var captured *freshSandboxReservationSnapshot
	err := withSandboxReceiptObservation(ctx, c, func(owned context.Context) error {
		source, err := captureHostStateSourceWitness(owned, c)
		if err != nil {
			return err
		}
		return withSandboxFile(owned, c.Host, false, func(file *sandboxFile) error {
			parentRaw, exists := file.sessions[parentID]
			if !exists {
				return joinIntentError()
			}
			parent, err := decodeSandboxContext(owned, parentID, parentRaw, file.schema)
			if err != nil {
				return err
			}
			rootID := localSandboxRoot(&parent)
			rootRaw, exists := file.sessions[rootID]
			if !exists {
				return joinIntentError()
			}
			root, err := decodeSandboxContext(owned, rootID, rootRaw, file.schema)
			if err != nil {
				return err
			}
			if err := validateFreshSandboxAncestors(&parent, &root, c.HostID()); err != nil {
				return err
			}
			captured = &freshSandboxReservationSnapshot{conn: c, host: cloneGrantHost(c.Host), source: source.clone(),
				parent: cloneSandboxPolicy(&parent), root: cloneSandboxPolicy(&root), parentRaw: bytes.Clone(parentRaw), rootRaw: bytes.Clone(rootRaw), schema: file.schema}
			return nil
		})
	})
	return captured, err
}

func validateFreshSandboxAncestors(parent, root *SandboxPolicy, hostID string) error {
	if parent == nil || root == nil || !ValidHostID(hostID) || parent.HostID != hostID || root.HostID != hostID ||
		parent.FutureLineage || root.FutureLineage || parent.FutureDelivery || root.FutureDelivery || parent.SSHAgent || root.SSHAgent ||
		parent.CreationWall == "none" || parent.CreationWall != root.CreationWall || root.lineage != nil ||
		localSandboxRoot(parent) != root.SessionID || !parent.Ceiling.Valid() || !root.Ceiling.Valid() ||
		len(parent.PolicySources) == 0 || !bytes.Equal(parent.PolicySources, root.PolicySources) {
		return joinIntentError()
	}
	return nil
}

func cloneFreshSandboxSnapshot(s *freshSandboxReservationSnapshot) *freshSandboxReservationSnapshot {
	if s == nil {
		return nil
	}
	q := *s
	q.host, q.source = cloneGrantHost(s.host), s.source.clone()
	q.parent, q.root = cloneSandboxPolicy(s.parent), cloneSandboxPolicy(s.root)
	q.parentRaw, q.rootRaw = bytes.Clone(s.parentRaw), bytes.Clone(s.rootRaw)
	return &q
}

func (s *freshSandboxReservationSnapshot) check(ctx context.Context, c *Conn) error {
	if s == nil || s.conn != c || c == nil || !SameEntry(s.host, c.Host) {
		return joinIntentError()
	}
	if err := validateFreshSandboxAncestors(s.parent, s.root, c.HostID()); err != nil {
		return err
	}
	return s.source.check(ctx, c)
}
