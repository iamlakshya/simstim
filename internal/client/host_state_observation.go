package client

import (
	"context"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
)

type hostStateRole uint8

const (
	hostStateSaved hostStateRole = iota + 1
	hostStateRegistration
	hostStateRead
	hostStatePublication
	hostStateCreation
)

type hostStateObservationKey struct{}

// HostStateObservation binds an actual observation to its original operation
// or owned registration stage. It carries no state JSON or secret values.
// Rebinding this token never renews a closed owner or an old generation.
type HostStateObservation struct {
	mu                sync.Mutex
	role              hostStateRole
	name              string
	key               string
	stateDir          string
	hostsPath         string
	source            hostStateSource
	original          *Host
	prospective       Host
	expectedID        string
	hostID            string
	checked           bool
	checkedConn       *Conn
	legacyConn        *Conn
	transport         *hostTransportStamp
	publicationResult *Host
	op                *heldLock
	stage             *stageBinding
	publication       atomic.Bool
	publicationOP     atomic.Pointer[heldLock]
	creation          *hostCreationReservation
	capturedOP        atomic.Pointer[heldLock]
}

// ObserveHostStateContext must run before the probe or action whose reply will
// be recorded. It requires the caller's existing live operation. A producer
// must not use it to wrap a result which was returned under an earlier owner.
func ObserveHostStateContext(ctx context.Context, h Host, hostID string) (*HostStateObservation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ctx.Value(hostMutationContextKey{}) != nil || ctx.Value(hostForgetMembershipKey{}) != nil {
		return nil, hostStateRemovalError("a new observation cannot be minted inside metadata mutation or cleanup")
	}
	if !ValidHostName(h.Name) || hostID != "" && !ValidHostID(hostID) {
		return nil, hostStateRemovalError("the state observation has an invalid host identity")
	}
	held, _ := ctx.Value(heldKey{h.Name}).(*heldLock)
	if held == nil || held.gone.Load() || held.terminal.Load() {
		return nil, hostStateRemovalError("the state observation needs its original live host operation")
	}
	if err := held.checkStateOperation(ctx); err != nil {
		return nil, err
	}
	observeCtx, release, err := held.stateLifetime().enterTransaction(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	var observation *HostStateObservation
	err = withHostMembershipContext(observeCtx, func(locked context.Context) error {
		if held.gone.Load() {
			return hostStateRemovalError("the state observation operation ended")
		}
		hosts, err := LoadHosts()
		if err != nil {
			return err
		}
		i := slices.IndexFunc(hosts, func(current Host) bool { return current.Name == h.Name })
		if i < 0 || !SameEntry(hosts[i], h) {
			return hostStateRemovalError("the saved host changed before the state observation")
		}
		source, err := captureHostStateSource(locked, hosts, true)
		if err != nil {
			return err
		}
		original := cloneObservationHost(h)
		observation = &HostStateObservation{role: hostStateSaved, name: h.Name, key: HostFileKey(h.Name), stateDir: StateDir(),
			hostsPath: hostsPath(), source: source, original: &original, prospective: cloneObservationHost(h), expectedID: hostID, op: held}
		return nil
	})
	return observation, err
}

// ObserveHostRegistrationContext captures Was and Now before transport reads.
// Only an explicit owned stage is accepted. The global current stage is not a
// substitute. Publishing the host later expires this source generation.
func ObserveHostRegistrationContext(ctx context.Context, c HostChange) (*HostStateObservation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ctx.Value(hostMutationContextKey{}) != nil || ctx.Value(hostForgetMembershipKey{}) != nil {
		return nil, hostStateRemovalError("a registration observation cannot begin inside metadata mutation or cleanup")
	}
	if c.Now == nil || !ValidHostName(c.Now.Name) || c.Was != nil && c.Was.Name != c.Now.Name {
		return nil, hostStateRemovalError("the registration observation needs its actual host change")
	}
	var binding *stageBinding
	for current, _ := ctx.Value(stageBindingKey{}).(*stageBinding); current != nil; current = current.prev {
		if current.name == c.Now.Name {
			binding = current
			break
		}
	}
	if binding == nil || binding.st == nil {
		return nil, hostStateRemovalError("the registration observation needs its owned host key stage")
	}
	if err := sameStage(c.Now.Name, binding.st); err != nil {
		return nil, err
	}
	var observation *HostStateObservation
	err := withHostMembershipContext(ctx, func(locked context.Context) error {
		hosts, err := LoadHosts()
		if err != nil {
			return err
		}
		i := slices.IndexFunc(hosts, func(current Host) bool { return current.Name == c.Now.Name })
		if c.Was == nil && i >= 0 || c.Was != nil && (i < 0 || !SameEntry(hosts[i], *c.Was)) {
			return hostStateRemovalError("saved hosts changed before the registration observation")
		}
		source, err := captureHostStateSource(locked, hosts, true)
		if err != nil {
			return err
		}
		observation = &HostStateObservation{role: hostStateRegistration, name: c.Now.Name, key: HostFileKey(c.Now.Name), stateDir: StateDir(),
			hostsPath: hostsPath(), source: source, prospective: cloneObservationHost(*c.Now), stage: binding}
		if c.Was != nil {
			original := cloneObservationHost(*c.Was)
			observation.original = &original
		}
		return sameStage(c.Now.Name, binding.st)
	})
	return observation, err
}

// WithHostStateObservation carries the same token on a fresh runtime context.
// The original OP or stage remains the owner. Validation happens at admission,
// so a nil, expired, changed-generation or wrong-root token grants no access.
func WithHostStateObservation(ctx context.Context, observation *HostStateObservation) context.Context {
	ctx = context.WithValue(ctx, hostStateObservationKey{}, observation)
	if observation == nil {
		return ctx
	}
	if observation.op != nil {
		ctx = context.WithValue(ctx, heldKey{observation.name}, observation.op)
	}
	if op := observation.capturedOP.Load(); op != nil {
		ctx = context.WithValue(ctx, heldKey{observation.name}, op)
	}
	if observation.creation != nil {
		if op := observation.creation.op.Load(); op != nil {
			ctx = context.WithValue(ctx, heldKey{observation.name}, op)
		}
	}
	if observation.stage != nil {
		ctx = context.WithValue(ctx, stageBindingKey{}, observation.stage)
	}
	return ctx
}

func cloneObservationHost(h Host) Host { return cloneForgetHost(h) }

func (o *HostStateObservation) checkOwner(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if o == nil || !ValidHostName(o.name) || o.key != HostFileKey(o.name) || o.stateDir != StateDir() || o.hostsPath != hostsPath() {
		return hostStateRemovalError("the state observation belongs to another local host domain")
	}
	if !o.source.exists && o.role != hostStateRegistration && o.role != hostStatePublication && o.role != hostStateCreation {
		return hostStateRemovalError("this observation role cannot use absent membership")
	}
	switch o.role {
	case hostStateSaved, hostStateRead:
		if o.op == nil || o.op.gone.Load() || o.op.terminal.Load() {
			return hostStateRemovalError("the original state observation operation ended")
		}
		return o.op.checkStateOperation(ctx)
	case hostStateRegistration:
		if op := o.capturedOP.Load(); op != nil {
			if op.gone.Load() || op.terminal.Load() {
				return hostStateRemovalError("the registration payload original operation ended")
			}
			if err := op.checkStateOperation(ctx); err != nil {
				return err
			}
		}
		if o.stage == nil || o.stage.st == nil {
			return hostStateRemovalError("the registration observation lost its owned stage")
		}
		return sameStage(o.name, o.stage.st)
	case hostStatePublication:
		if !o.publication.Load() {
			return hostStateRemovalError("the metadata publication scope ended")
		}
		if op := o.publicationOP.Load(); op != nil {
			if op.gone.Load() || op.terminal.Load() {
				return hostStateRemovalError("the metadata publication operation ended")
			}
			return op.checkStateOperation(ctx)
		}
	case hostStateCreation:
		if o.creation == nil || o.creation.stage == nil || o.creation.stage.st == nil {
			return hostStateRemovalError("the creation reservation has no actual owned empty-stage handle")
		}
		if op := o.creation.op.Load(); op != nil {
			if op.gone.Load() || op.terminal.Load() {
				return hostStateRemovalError("the provider creation operation ended")
			}
			if err := op.checkStateOperation(ctx); err != nil {
				return err
			}
		}
		return sameStage(o.name, o.creation.stage.st)
	default:
		return hostStateRemovalError("the state observation role is unknown")
	}
	return nil
}

// checkMembership runs only while the canonical membership context is held.
func (o *HostStateObservation) checkMembership(ctx context.Context) error {
	if err := requireHostMembershipContext(ctx); err != nil {
		return err
	}
	if err := o.checkOwner(ctx); err != nil {
		return err
	}
	if err := o.source.check(ctx); err != nil {
		return err
	}
	// The constructor compared the actual entry under this exact generation.
	// Every canonical membership write rotates it, including identical writes.
	// Do not rematerialise all hosts for every descriptor snapshot check.
	return o.checkOwner(ctx)
}

func (o *HostStateObservation) permits(subdirs []string, name string, write bool) bool {
	if o == nil || len(subdirs) != 2 || subdirs[0] != "hosts" || subdirs[1] != o.key || filepath.Base(name) != name {
		return false
	}
	if o.role == hostStatePublication || o.role == hostStateCreation {
		return false
	}
	if o.role == hostStateRead {
		return !write && slices.Contains(hostStateFiles[:], name)
	}
	if o.role == hostStateRegistration {
		if !write && name == "sandboxes.json" {
			return true
		}
		return name == "baseline.json" || name == "root-steps.json" || name == "removal.json"
	}
	return slices.Contains(hostStateFiles[:], name)
}

// bindHostStateOwnerReceipt is used only by the canonical H15 owner adapter
// after its complete, original-bound Removal.Purge receipt is verified. It is
// private so a front end cannot turn a raw tier ID into checked identity.
func bindHostStateOwnerReceipt(ctx context.Context, h Host, hostID string) error {
	o, _ := ctx.Value(hostStateObservationKey{}).(*HostStateObservation)
	if o == nil || o.role != hostStateSaved {
		return hostStateRemovalError("a verified removal receipt needs its original saved-host observation")
	}
	return bindHostStateIdentity(ctx, o, h, hostID, nil)
}

func (o *HostStateObservation) hasCheckedIdentity() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.checked && ValidHostID(o.hostID)
}

// captureHostStateObservation is for the actual observation producer before
// reading a reply. Returned payloads retain this pointer privately. A later
// Save or Record checks that same pointer rather than minting another token.
func captureHostStateObservation(ctx context.Context, h Host) (*HostStateObservation, error) {
	o, _ := ctx.Value(hostStateObservationKey{}).(*HostStateObservation)
	if err := o.checkOwner(ctx); err != nil {
		return nil, err
	}
	if o.role == hostStatePublication || o.role == hostStateRead || o.role == hostStateCreation || !SameEntry(o.prospective, h) {
		return nil, hostStateRemovalError("the payload has no matching original observation")
	}
	if o.role == hostStateRegistration {
		held, _ := ctx.Value(heldKey{o.name}).(*heldLock)
		if held == nil || held.terminal.Load() {
			return nil, hostStateRemovalError("registration payload capture needs its actual original OP before the remote read")
		}
		if err := held.checkStateOperation(ctx); err != nil {
			return nil, err
		}
		if !o.capturedOP.CompareAndSwap(nil, held) && o.capturedOP.Load() != held {
			return nil, hostStateRemovalError("the registration payload belongs to another original OP")
		}
	}
	if err := withHostMembershipContext(ctx, o.checkMembership); err != nil {
		return nil, err
	}
	return o, nil
}

func requireHostStatePayloadObservation(ctx context.Context, h Host, captured *HostStateObservation) error {
	o, err := captureHostStateObservation(ctx, h)
	if err != nil {
		return err
	}
	if captured == nil || o != captured {
		return hostStateRemovalError("the payload belongs to another observation")
	}
	return nil
}

func requireRecordedHostStateObservation(ctx context.Context, h Host, hostID string, captured *HostStateObservation) error {
	if err := requireHostStatePayloadObservation(ctx, h, captured); err != nil {
		return err
	}
	o := captured
	o.mu.Lock()
	boundID := o.hostID
	checked := o.checked
	o.mu.Unlock()
	if hostID == "" {
		if o.role != hostStateRegistration || boundID != "" || checked {
			return hostStateRemovalError("the payload has no checked host identity")
		}
	} else if !checked || !ValidHostID(hostID) || boundID != hostID {
		return hostStateRemovalError("the payload identity differs from its checked observation")
	}
	return o.checkOwner(ctx)
}
