package client

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// HostCreationRequest copies the explicit provider request before the provider
// runs. The caller keeps its progress writer separately. No value is persisted
// or logged, and TokenCommand is not executed by these helpers.
type HostCreationRequest struct {
	Name, Provider                          string
	CPUs                                    int
	Memory, Disk                            int64
	Image, Template, IdleStop, TokenCommand string
}

type hostCreationReservation struct {
	request               HostCreationRequest
	stage                 *stageBinding
	op                    atomic.Pointer[heldLock]
	bound                 bool                        // protected by the containing observation's mu
	started               bool                        // protected by the containing observation's mu
	preflight             *hostCreationPreflightProof // protected by the containing observation's mu
	sourceScope           *HostCreationSourceScope
	sourceBirth           *hostMachineSourceBirth
	sourceRecordAttempted bool
	sourceProducerEntered bool
}

// BeginHostCreationContext owns only an empty key stage and the actual OP.
// Empty is not checked pin identity. The caller must immediately defer release
// and still owns every provider action, partial response and cleanup outcome.
func BeginHostCreationContext(ctx context.Context, request HostCreationRequest) (context.Context, func(), error) {
	freezeHostMachineSourceObservers()
	if err := ctx.Err(); err != nil {
		return ctx, nil, err
	}
	if !ValidHostName(request.Name) || request.Provider == "" {
		return ctx, nil, hostStateRemovalError("the creation reservation needs its actual name and selected provider")
	}
	if existing, _ := ctx.Value(hostStateObservationKey{}).(*HostStateObservation); existing != nil {
		if existing.role != hostStateCreation || existing.creation == nil || existing.creation.request != request {
			return ctx, nil, hostStateRemovalError("the provider request differs from its existing creation reservation")
		}
		if err := existing.checkOwner(ctx); err != nil {
			return ctx, nil, err
		}
		existing.mu.Lock()
		started := existing.creation.started
		existing.mu.Unlock()
		if started {
			return ctx, nil, hostStateRemovalError("the creation reservation already entered its provider attempt")
		}
		if err := withHostMembershipContext(ctx, existing.checkMembership); err != nil {
			return ctx, nil, err
		}
		owned, _ := ctx.Value(heldKey{request.Name}).(*heldLock)
		if owned == nil || owned != existing.creation.op.Load() {
			return ctx, nil, hostStateRemovalError("the creation reservation lost its actual OP")
		}
		return ctx, func() {}, nil
	}
	// Refuse an already-saved name before superseding any older empty stage.
	if err := withHostMembershipContext(ctx, func(context.Context) error {
		hosts, err := LoadHosts()
		if err != nil {
			return err
		}
		if slices.ContainsFunc(hosts, func(h Host) bool { return h.Name == request.Name }) {
			return hostStateRemovalError("a saved host already uses the requested creation name")
		}
		return nil
	}); err != nil {
		return ctx, nil, err
	}
	staged, abort, err := StagePinOwned(ctx, request.Name)
	if err != nil {
		return ctx, nil, err
	}
	var observation *HostStateObservation
	err = withHostMembershipContext(staged, func(locked context.Context) error {
		hosts, err := LoadHosts()
		if err != nil {
			return err
		}
		if slices.ContainsFunc(hosts, func(h Host) bool { return h.Name == request.Name }) {
			return hostStateRemovalError("the creation name was saved before its reservation")
		}
		source, err := captureHostStateSource(locked, hosts, true)
		if err != nil {
			return err
		}
		binding, _ := staged.Value(stageBindingKey{}).(*stageBinding)
		if binding == nil || binding.name != request.Name || binding.st == nil {
			return hostStateRemovalError("the creation did not obtain its actual owned stage")
		}
		observation = &HostStateObservation{role: hostStateCreation, name: request.Name, key: HostFileKey(request.Name),
			stateDir: StateDir(), hostsPath: hostsPath(), source: source, prospective: Host{Name: request.Name}, stage: binding,
			creation: &hostCreationReservation{request: request, stage: binding}}
		return observation.checkOwner(locked)
	})
	if err != nil {
		abort()
		return ctx, nil, err
	}
	owned, unlock, err := LockHost(WithHostStateObservation(staged, observation), request.Name, "host create")
	if err != nil {
		abort()
		return ctx, nil, err
	}
	release := sync.OnceFunc(func() {
		// No membership lease is held here. The exact old abort cannot remove
		// a new connect/setup stage which superseded this provider stage.
		abort()
		unlock()
	})
	return owned, release, nil
}

// StartHostCreationContext is called once, after existing-journal preflight and
// immediately before the actual provider.Create. It creates no resource and
// authorises neither another provider attempt nor any host JSON write.
func StartHostCreationContext(ctx context.Context) error {
	o, _ := ctx.Value(hostStateObservationKey{}).(*HostStateObservation)
	if o == nil || o.role != hostStateCreation || o.creation == nil {
		return hostStateRemovalError("the provider attempt has no creation reservation")
	}
	owned, _ := ctx.Value(heldKey{o.name}).(*heldLock)
	if owned == nil || owned != o.creation.op.Load() {
		return hostStateRemovalError("the provider attempt lost its actual OP")
	}
	if err := owned.checkStateOperation(ctx); err != nil {
		return err
	}
	return withHostMembershipContext(ctx, func(locked context.Context) error {
		if err := o.checkMembership(locked); err != nil {
			return err
		}
		if err := verifyHostCreationPreflight(locked, o); err != nil {
			return err
		}
		o.mu.Lock()
		defer o.mu.Unlock()
		if o.creation.started {
			return hostStateRemovalError("the creation reservation already entered its provider attempt")
		}
		o.creation.started = true
		o.creation.sourceScope = &HostCreationSourceScope{observation: o, op: owned, stage: o.stage, request: o.creation.request}
		return o.checkOwner(locked)
	})
}

// BindHostCreationResultContext binds only this attempt's actual provider
// response for metadata publication. It supplies no checked SSH identity and
// no state JSON authority. The caller must preserve and report any externally
// created resource even when binding or the later CAS returns an error.
func BindHostCreationResultContext(ctx context.Context, result Host) (Host, error) {
	o, _ := ctx.Value(hostStateObservationKey{}).(*HostStateObservation)
	if o == nil || o.role != hostStateCreation || o.creation == nil {
		return result, hostStateRemovalError("the provider result has no original creation reservation")
	}
	request := o.creation.request
	if result.Name != request.Name || result.Machine == nil || result.Machine.Provider != request.Provider || result.Machine.ID == "" {
		return result, hostStateRemovalError("the provider result differs from the actual creation request")
	}
	owned, _ := ctx.Value(heldKey{o.name}).(*heldLock)
	if owned == nil || owned != o.creation.op.Load() {
		return result, hostStateRemovalError("the provider result lost its original OP")
	}
	if err := owned.checkStateOperation(ctx); err != nil {
		return result, err
	}
	if err := o.checkOwner(ctx); err != nil {
		return result, err
	}
	err := withHostMembershipContext(ctx, func(locked context.Context) error {
		if err := o.checkMembership(locked); err != nil {
			return err
		}
		o.mu.Lock()
		defer o.mu.Unlock()
		if !o.creation.started {
			return hostStateRemovalError("the result did not follow its reserved provider attempt")
		}
		if err := checkHostMachineSourceBirthBindingLocked(o, result); err != nil {
			return err
		}
		if o.creation.bound {
			if !SameEntry(o.prospective, result) {
				return hostStateRemovalError("the creation attempt already bound another result")
			}
			result = cloneObservationHost(o.prospective)
			return nil
		}
		result = cloneObservationHost(result)
		result.Machine.Created = time.Now().UTC().Format(time.RFC3339Nano)
		bindHostMachineSourceBirthLocked(o, result)
		o.prospective = cloneObservationHost(result)
		o.creation.bound = true
		return o.checkOwner(locked)
	})
	return result, err
}
