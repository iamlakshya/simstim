package client

import (
	"context"
	"errors"
	"slices"
	"sync"

	"github.com/iamlakshya/simstim/internal/brand"
)

type savedHostRemovalKeyV1 struct{}

// SavedHostRemovalV1 owns one outside removal of an actual saved host. It
// carries the original native operation, canonical membership Source and local
// host identity. It grants no provider, root, sandbox or credential operation.
type SavedHostRemovalV1 struct {
	mu                     sync.Mutex
	ctx                    context.Context
	release                func()
	op                     *heldLock
	host                   Host
	source                 hostStateSource
	tier                   TierEntry
	hasTier                bool
	stateDir               string
	busy, closed, prepared bool
	participants           []*savedHostRemovalOwnerV1
	factories              []savedHostRemovalFactoryV1
	attempt                *savedHostRemovalAttemptV1
}

// OpenSavedHostRemovalV1 acquires the host operation and captures the current
// saved Source and HostID before any removal participant or provider runs.
func OpenSavedHostRemovalV1(ctx context.Context, name string) (*SavedHostRemovalV1, error) {
	return openSavedHostRemovalV1(ctx, name, savedHostRemovalFactoriesV1())
}

func openSavedHostRemovalV1(ctx context.Context, name string, factories []savedHostRemovalFactoryV1) (*SavedHostRemovalV1, error) {
	if ctx == nil || !ValidHostName(name) {
		return nil, savedHostRemovalErrorV1("removal needs its original saved host name")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ctx.Value(savedHostRemovalKeyV1{}) != nil || ctx.Value(hostMembershipContextKey{}) != nil || ctx.Value(hostMutationContextKey{}) != nil || ctx.Value(hostStateObservationKey{}) != nil || ctx.Value(hostForgetMembershipKey{}) != nil || ctx.Value(hostForgetIntentKey{}) != nil || ctx.Value(stageBindingKey{}) != nil || ctx.Value(hostTransportAttemptKey{}) != nil || grantRequestOf(ctx) != nil {
		return nil, savedHostRemovalErrorV1("a new saved-host removal cannot borrow another operation's purpose")
	}
	owned, release, err := LockHost(ctx, name, "host rm")
	if err != nil {
		return nil, err
	}
	transaction := &SavedHostRemovalV1{ctx: owned, release: release, host: Host{Name: name}, stateDir: StateDir(), factories: slices.Clone(factories)}
	transaction.op, _ = owned.Value(heldKey{name}).(*heldLock)
	if transaction.op == nil || !transaction.op.savedRemoval.CompareAndSwap(nil, transaction) {
		release()
		return nil, savedHostRemovalErrorV1("the actual host operation already owns another removal producer")
	}
	fail := func(err error) (*SavedHostRemovalV1, error) {
		transaction.op.savedRemoval.CompareAndSwap(transaction, nil)
		release()
		return nil, err
	}
	if err := ensureTiersMigrated(owned); err != nil {
		return fail(err)
	}
	err = withHostMembershipContext(owned, func(locked context.Context) error {
		hosts, err := LoadHosts()
		if err != nil {
			return err
		}
		index := slices.IndexFunc(hosts, func(h Host) bool { return h.Name == name })
		if index < 0 {
			return &UserError{Msg: "no saved host called " + CleanLine(name), Hint: brand.Name + " host ls", Code: "host_not_found", Kind: KindNotFound}
		}
		source, err := captureHostStateSource(locked, hosts, true)
		if err != nil {
			return err
		}
		if !source.exists || !ValidHostID(source.generation) {
			return savedHostRemovalErrorV1("the removal has no actual saved membership Source")
		}
		tiers, err := readTiersFile()
		if err != nil {
			return err
		}
		entry, exists := tiers.Entry(name)
		if entry.HostID != "" && !ValidHostID(entry.HostID) {
			return grantIdentityError(hosts[index])
		}
		transaction.host, transaction.source, transaction.tier, transaction.hasTier = cloneObservationHost(hosts[index]), source, entry, exists
		return transaction.checkLockedV1(locked, false)
	})
	if err != nil {
		return fail(err)
	}
	transaction.ctx = context.WithValue(owned, savedHostRemovalKeyV1{}, transaction)
	return transaction, nil
}

// Context retains this removal's actual host operation, without donating any
// root, provider, sandbox or remote-grant purpose.
func (r *SavedHostRemovalV1) Context() context.Context {
	if r == nil {
		return nil
	}
	return r.ctx
}

// Host returns reporting data from the original saved Source.
func (r *SavedHostRemovalV1) Host() Host {
	if r == nil {
		return Host{}
	}
	return cloneObservationHost(r.host)
}

// HostID returns the identity captured from the original local saved tier.
func (r *SavedHostRemovalV1) HostID() string {
	if r == nil {
		return ""
	}
	return r.tier.HostID
}

// Check revalidates the original operation, native roots, host entry and tier.
func (r *SavedHostRemovalV1) Check(ctx context.Context) error {
	if err := r.checkOperationV1(ctx); err != nil {
		return err
	}
	return withHostMembershipContext(ctx, func(locked context.Context) error { return r.checkLockedV1(locked, false) })
}

// CheckConnection accepts only a real saved-host connection from this exact
// Source and original HostID. It creates no remote mutation permission.
func (r *SavedHostRemovalV1) CheckConnection(ctx context.Context, conn *Conn) error {
	if err := r.Check(ctx); err != nil {
		return err
	}
	if conn == nil || conn.agentOnly || conn.hostTransport == nil || conn.hostTransport.kind != hostTransportSaved || !SameEntry(conn.Host, r.host) || !ValidHostID(conn.HostID()) || conn.HostID() != r.tier.HostID || conn.HostIDChanged() || !sameHostTransportSource(conn.hostTransport.source, r.source) {
		return savedHostRemovalErrorV1("the removal connection does not belong to its original saved Source and HostID")
	}
	return conn.hostTransport.checkConn(conn)
}

func (r *SavedHostRemovalV1) checkOperationV1(ctx context.Context) error {
	if r == nil || ctx == nil {
		return savedHostRemovalErrorV1("the saved-host removal has no original owner")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	held, _ := ctx.Value(heldKey{r.host.Name}).(*heldLock)
	if closed || r.op == nil || held != r.op || r.op.savedRemoval.Load() != r || r.stateDir != StateDir() || r.source.path != hostsPath() {
		return savedHostRemovalErrorV1("the saved-host removal lost its original native operation or location")
	}
	return held.checkStateOperation(ctx)
}

func (r *SavedHostRemovalV1) checkLockedV1(ctx context.Context, committed bool) error {
	if err := requireHostMembershipContext(ctx); err != nil {
		return err
	}
	if err := r.checkOperationV1(ctx); err != nil {
		return err
	}
	source := r.source
	if committed {
		if r.attempt == nil || !r.attempt.committed || !r.attempt.post.exists {
			return savedHostRemovalErrorV1("the removal has no actual committed writer Source")
		}
		source = r.attempt.post
	}
	if err := source.check(ctx); err != nil {
		return err
	}
	hosts, err := LoadHosts()
	if err != nil {
		return err
	}
	index := slices.IndexFunc(hosts, func(h Host) bool { return h.Name == r.host.Name })
	if committed {
		if index >= 0 {
			return savedHostRemovalErrorV1("a host now uses the original removed name")
		}
		tiers, err := readTiersFile()
		if err != nil {
			return err
		}
		entry, exists := tiers.Entry(r.host.Name)
		if exists != r.attempt.postHasTier || !sameRemovalTierV1(entry, r.attempt.postTier) {
			return savedHostRemovalErrorV1("the committed host identity changed before native finalisation")
		}
		return nil
	}
	if index < 0 || !SameEntry(hosts[index], r.host) {
		return savedHostRemovalErrorV1("the original saved host changed during removal")
	}
	tiers, err := readTiersFile()
	if err != nil {
		return err
	}
	entry, exists := tiers.Entry(r.host.Name)
	if exists != r.hasTier || !sameRemovalTierV1(entry, r.tier) {
		return savedHostRemovalErrorV1("the original host identity or tier changed during removal")
	}
	return ctx.Err()
}

func sameRemovalTierV1(a, b TierEntry) bool {
	return a.Name == b.Name && a.Tier == b.Tier && a.HostID == b.HostID && a.Key == b.Key && a.SetAt.Equal(b.SetAt) && a.IDChanged == b.IDChanged && a.NeedsAnswer == b.NeedsAnswer
}

func (r *SavedHostRemovalV1) beginV1(ctx context.Context) (func(), error) {
	if err := r.checkOperationV1(ctx); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.busy {
		return nil, savedHostRemovalErrorV1("the original removal is closed or still owns an active step")
	}
	r.busy = true
	return func() { r.mu.Lock(); r.busy = false; r.mu.Unlock() }, nil
}

// Close releases the actual operation only after rollback or finalisation has
// completed and every participant has positively released its native handles.
func (r *SavedHostRemovalV1) Close() error {
	if r == nil {
		return nil
	}
	if r.op == nil || r.release == nil {
		return savedHostRemovalErrorV1("the removal has no original native owner to close")
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	if r.busy || r.attempt != nil && r.attempt.committed && r.nativePendingV1() {
		r.mu.Unlock()
		return savedHostRemovalErrorV1("the original removal still owns local configuration cleanup")
	}
	r.busy = true
	r.mu.Unlock()
	if r.attempt != nil && !r.attempt.committed && !r.attempt.rolledBack && len(r.participants) != 0 {
		rollback, cancel := removalRollbackContextV1(r.ctx, r.attempt)
		err := withHostMembershipContext(rollback, func(locked context.Context) error { return r.rollbackLockedV1(locked) })
		cancel()
		if err != nil {
			r.mu.Lock()
			r.busy = false
			r.mu.Unlock()
			return err
		}
	}
	var failure error
	for _, owner := range r.participants {
		failure = errors.Join(failure, owner.participant.close())
	}
	r.mu.Lock()
	r.busy = false
	if failure == nil {
		r.closed = true
	}
	r.mu.Unlock()
	if failure == nil {
		r.op.savedRemoval.CompareAndSwap(r, nil)
		r.release()
	}
	return failure
}

func (r *SavedHostRemovalV1) nativePendingV1() bool {
	for _, owner := range r.participants {
		if owner.staged && !owner.finalised {
			return true
		}
	}
	return false
}

func savedHostRemovalErrorV1(message string) error {
	return &UserError{Msg: message, Hint: "Inspect the current saved host and any reported local files before retrying removal.", Code: "host_removal_source", Kind: KindRefused}
}
