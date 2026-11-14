package client

import (
	"context"
	"slices"
	"sync/atomic"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

type hostStateInspectionKey struct{}

// This purpose-bound scope is not exposed to the owner callback. It permits
// only the fixed existing journal transaction for canonical creation preflight.
// The creation token itself still grants no JSON read or write authority.
type hostStateInspection struct {
	observation *HostStateObservation
	op          *heldLock
	active      atomic.Bool
}

type hostCreationPreflightProof struct {
	anchors []stateRootAnchor
	root    *cachefs.Reference
	data    *cachefs.Reference
	lock    *cachefs.Reference
}

func withHostCreationRemovalInspection(ctx context.Context, name string, read func(stateFileView) error) error {
	if read == nil {
		return hostStateRemovalError("creation preflight has no canonical owner inspection")
	}
	return withHostCreationRemovalState(ctx, name, func(tx *stateFileTxn) error { return read(tx.view()) })
}

// The inspection and fixed clearing purpose share this ONE original
// reservation/lifetime/member/native transaction and preflight publication.
func withHostCreationRemovalState(ctx context.Context, name string, run func(*stateFileTxn) error) error {
	o, _ := ctx.Value(hostStateObservationKey{}).(*HostStateObservation)
	if o == nil || o.role != hostStateCreation || o.creation == nil || o.name != name || run == nil {
		return hostStateRemovalError("creation preflight has no exact reservation")
	}
	if err := o.checkOwner(ctx); err != nil {
		return err
	}
	owned, _ := ctx.Value(heldKey{name}).(*heldLock)
	if owned == nil || owned != o.creation.op.Load() {
		return hostStateRemovalError("creation preflight lost its actual OP")
	}
	viewctx, leave, err := owned.stateLifetime().enterTransaction(ctx)
	if err != nil {
		return err
	}
	defer leave()
	return withHostMembershipContext(viewctx, func(locked context.Context) error {
		if err := o.checkMembership(locked); err != nil {
			return err
		}
		o.mu.Lock()
		started := o.creation.started
		if !started {
			// A failed new inspection/clear must not retain an old proof.
			o.creation.preflight = nil
		}
		o.mu.Unlock()
		if started {
			return hostStateRemovalError("creation journal preflight must precede the provider attempt")
		}
		scope := &hostStateInspection{observation: o, op: owned}
		scope.active.Store(true)
		defer scope.active.Store(false)
		bounded := context.WithValue(locked, hostStateInspectionKey{}, scope)
		var proof *hostCreationPreflightProof
		err := withStateTransaction(bounded, []string{"hosts", HostFileKey(name)}, "removal.json", false, true, func(tx *stateFileTxn) error {
			var err error
			proof, err = captureHostCreationPreflight(tx)
			if err != nil {
				return err
			}
			if err := run(tx); err != nil {
				return err
			}
			if tx.removed {
				// Only the same checked unlink and successful source close can
				// establish absence; keep the ORIGINAL root/sentinel refs.
				proof.data = nil
			}
			return checkHostCreationPreflightSnapshot(bounded, tx, proof)
		})
		// withStateTransaction has now revoked views and closed all owned
		// source/sentinel/root handles. No close/flush error may publish proof.
		if err != nil {
			return err
		}
		if err := o.checkMembership(locked); err != nil {
			return err
		}
		o.mu.Lock()
		defer o.mu.Unlock()
		if err := locked.Err(); err != nil {
			return err
		}
		if o.creation.started {
			return hostStateRemovalError("the creation provider phase changed before preflight publication")
		}
		o.creation.preflight = proof
		return nil
	})
}

func captureHostCreationPreflight(tx *stateFileTxn) (*hostCreationPreflightProof, error) {
	proof := &hostCreationPreflightProof{anchors: slices.Clone(tx.anchors)}
	if tx.root == nil {
		return proof, nil
	}
	root, err := cachefs.CaptureRoot(tx.root)
	if err != nil {
		return nil, err
	}
	proof.root = &root
	if tx.source != nil {
		data, err := cachefs.CaptureFile(tx.root, tx.name, tx.source, 0600)
		if err != nil {
			return nil, err
		}
		proof.data = &data
	}
	if tx.lock != nil {
		lock, err := cachefs.CaptureFile(tx.root, tx.lock.name, tx.lock.file, 0600)
		if err != nil {
			return nil, err
		}
		proof.lock = &lock
	}
	return proof, nil
}

func verifyHostCreationPreflight(ctx context.Context, o *HostStateObservation) error {
	o.mu.Lock()
	proof := o.creation.preflight
	o.mu.Unlock()
	if proof == nil {
		return hostStateRemovalError("the existing removal journal was not inspected before provider creation")
	}
	owned, _ := ctx.Value(heldKey{o.name}).(*heldLock)
	if owned == nil || owned != o.creation.op.Load() {
		return hostStateRemovalError("creation preflight lost its actual OP")
	}
	scope := &hostStateInspection{observation: o, op: owned}
	scope.active.Store(true)
	defer scope.active.Store(false)
	bounded := context.WithValue(ctx, hostStateInspectionKey{}, scope)
	return withStateTransaction(bounded, []string{"hosts", HostFileKey(o.name)}, "removal.json", false, true, func(tx *stateFileTxn) error {
		return checkHostCreationPreflightSnapshot(bounded, tx, proof)
	})
}

func checkHostCreationPreflightSnapshot(ctx context.Context, tx *stateFileTxn, proof *hostCreationPreflightProof) error {
	if err := tx.check(); err != nil {
		return err
	}
	if proof == nil {
		return hostStateRemovalError("creation preflight has no completed native proof")
	}
	if err := stateRootAnchorsMatch(ctx, tx.root, proof.anchors, tx.name); err != nil {
		return err
	}
	if (proof.root == nil) != (tx.root == nil) || (proof.data == nil) != (tx.source == nil) || (proof.lock == nil) != (tx.lock == nil) {
		return hostStateRemovalError("the inspected creation journal domain changed before provider creation")
	}
	if proof.root != nil {
		if err := proof.root.CheckRoot(tx.root); err != nil {
			return err
		}
	}
	if proof.data != nil {
		if err := proof.data.CheckFile(tx.root, tx.name, tx.source, 0600); err != nil {
			return err
		}
	}
	if proof.lock != nil {
		if err := proof.lock.CheckFile(tx.root, tx.lock.name, tx.lock.file, 0600); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (s *hostStateInspection) check(ctx context.Context, subdirs []string, name string, write, existingLock bool) error {
	if !s.active.Load() || write || !existingLock || len(subdirs) != 2 || subdirs[0] != "hosts" ||
		subdirs[1] != s.observation.key || name != "removal.json" {
		return hostStateRemovalError("the existing-journal inspection has another role or domain")
	}
	if s.op.gone.Load() || s.op != s.observation.creation.op.Load() {
		return hostStateRemovalError("the existing-journal inspection owner ended")
	}
	return s.observation.checkMembership(ctx)
}
