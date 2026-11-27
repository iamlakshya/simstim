package client

import (
	"context"
	"io"
	"sync/atomic"
)

// HostStateReceiptIdentity is returned only by the canonical owner's complete
// receipt inspector. It grants no authority without native OP and membership
// validation by the state backend.
type HostStateReceiptIdentity struct {
	Original Host
	HostID   string
	Nonce    string
	Proof    HostForgetMembershipProof
}

// HostStateReceiptInspector delegates raw receipt bytes to the one canonical
// owner decoder. The reader exposes no descriptor, close or mutation method.
type HostStateReceiptInspector func(context.Context, io.ReaderAt, int64, string) (HostStateReceiptIdentity, error)

type hostTerminalInspectionKey struct{}
type hostTerminalRetryKey struct{}

type hostTerminalInspection struct {
	name   string
	op     *heldLock
	active atomic.Bool
}

func (s *hostTerminalInspection) check(ctx context.Context) error {
	if !s.active.Load() || s.op.gone.Load() {
		return hostStateRemovalError("the existing-only terminal inspection ended")
	}
	if err := requireHostMembershipContext(ctx); err != nil {
		return err
	}
	return s.op.checkStateOperation(ctx)
}

func (s *hostTerminalInspection) permits(ctx context.Context, subdirs []string, name string, write, existingLock bool) error {
	if write || !existingLock || len(subdirs) != 2 || subdirs[0] != "hosts" || subdirs[1] != HostFileKey(s.name) || name != "removal.json" {
		return hostStateRemovalError("the terminal inspection has another state role or domain")
	}
	return s.check(ctx)
}

func inspectHostTerminalSource(ctx context.Context, scope *hostTerminalInspection, inspect HostStateReceiptInspector) (identity HostStateReceiptIdentity, exists bool, failure error) {
	bounded := context.WithValue(ctx, hostTerminalInspectionKey{}, scope)
	failure = withStateTransaction(bounded, []string{"hosts", HostFileKey(scope.name)}, "removal.json", false, true, func(tx *stateFileTxn) error {
		view := tx.view()
		if !view.Exists {
			return nil
		}
		var err error
		identity, err = inspect(ctx, view.ReaderAt, view.Size, scope.name)
		exists = err == nil
		return err
	})
	return identity, exists, failure
}

func validateHostTerminalIdentity(identity HostStateReceiptIdentity, name string) error {
	if !ValidHostName(name) || identity.Original.Name != name || !ValidHostID(identity.HostID) ||
		!ValidHostID(identity.Nonce) || !validHostForgetPair(identity.Proof) {
		return hostStateRemovalError("the terminal owner inspector returned no complete original identity")
	}
	return nil
}

func terminalIdentityMatches(header hostTerminalHeader, identity HostStateReceiptIdentity) bool {
	return header.Name == identity.Original.Name && header.Key == HostFileKey(identity.Original.Name) &&
		header.HostID == identity.HostID && header.Nonce == identity.Nonce &&
		header.From == identity.Proof.FromGeneration && header.Target == identity.Proof.TargetGeneration
}
