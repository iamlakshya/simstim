package client

import (
	"context"
	"sync"
)

// HostMachineSourceObserver is installed only by a fixed builtin's trusted
// initialisation. It must require its actual private Create frame, exact scope
// and context identity, request/result/run identity and original native pins.
// Observations run outside membership; typed metadata alone proves nothing.
type HostMachineSourceObserver func(context.Context, *HostCreationSourceScope, Host) (*MachineSourceProvenance, error)

var hostMachineSourceObservers = struct {
	sync.Mutex
	started bool
	values  map[string]HostMachineSourceObserver
}{values: make(map[string]HostMachineSourceObserver)}

// RegisterHostMachineSourceObserver installs one fixed builtin observer before
// any provider request. There is no replacement, request-time verifier or
// fallback observer. Direct SDK result DTOs cannot register during creation.
func RegisterHostMachineSourceObserver(provider string, observer HostMachineSourceObserver) error {
	hostMachineSourceObservers.Lock()
	defer hostMachineSourceObservers.Unlock()
	if provider != "docker" && provider != "lima" || observer == nil || hostMachineSourceObservers.started || hostMachineSourceObservers.values[provider] != nil {
		return hostStateRemovalError("the fixed machine-source observer cannot be installed or replaced here")
	}
	hostMachineSourceObservers.values[provider] = observer
	return nil
}

func freezeHostMachineSourceObservers() {
	hostMachineSourceObservers.Lock()
	hostMachineSourceObservers.started = true
	hostMachineSourceObservers.Unlock()
}

func hostMachineSourceObserver(provider string) (HostMachineSourceObserver, error) {
	hostMachineSourceObservers.Lock()
	defer hostMachineSourceObservers.Unlock()
	hostMachineSourceObservers.started = true
	observer := hostMachineSourceObservers.values[provider]
	if observer == nil || provider != "docker" && provider != "lima" {
		return nil, machineSourceUnknown()
	}
	return observer, nil
}

// HostCreationSourceScope is only an opaque handle on the original existing
// creation reservation. It grants no birth proof, provider command or sharing
// permission. Only the builtin's private actual Create frame can observe birth.
type HostCreationSourceScope struct {
	observation *HostStateObservation
	op          *heldLock
	stage       *stageBinding
	request     HostCreationRequest
}

// HostCreationSourceScopeContext returns the SAME started reservation scope.
// The builtin must obtain it ONCE before any command and keep its private frame
// tied to this pointer and exact request/context until Record completes.
func HostCreationSourceScopeContext(ctx context.Context) (*HostCreationSourceScope, error) {
	if err := checkHostMachineSourceObserverContext(ctx); err != nil {
		return nil, err
	}
	o, _ := ctx.Value(hostStateObservationKey{}).(*HostStateObservation)
	if o == nil || o.role != hostStateCreation || o.creation == nil {
		return nil, hostStateRemovalError("the provider source capture has no original creation reservation")
	}
	o.mu.Lock()
	scope := o.creation.sourceScope
	o.mu.Unlock()
	if scope == nil {
		return nil, hostStateRemovalError("the provider source capture did not follow the actual creation start")
	}
	if _, err := hostMachineSourceObserver(scope.request.Provider); err != nil {
		return nil, err
	}
	if err := scope.CheckContext(ctx); err != nil {
		return nil, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.creation.sourceProducerEntered || o.creation.sourceRecordAttempted || o.creation.sourceBirth != nil {
		return nil, hostStateRemovalError("the reserved provider already entered its one closed source Create")
	}
	o.creation.sourceProducerEntered = true
	return scope, nil
}

// Request returns immutable protected request data, never an observation fact.
func (s *HostCreationSourceScope) Request() HostCreationRequest {
	if s == nil {
		return HostCreationRequest{}
	}
	return s.request
}

// CheckContext checks the original OP/stage/Source and caller cancellation.
// It releases membership before returning to any provider/native observer.
func (s *HostCreationSourceScope) CheckContext(ctx context.Context) error {
	if err := checkHostMachineSourceObserverContext(ctx); err != nil {
		return err
	}
	if s == nil || s.observation == nil || ctx.Value(hostStateObservationKey{}) != s.observation {
		return hostStateRemovalError("the provider source scope belongs to another creation context")
	}
	return withHostMembershipContext(ctx, s.checkLocked)
}

func (s *HostCreationSourceScope) checkLocked(ctx context.Context) error {
	o := s.observation
	if err := o.checkMembership(ctx); err != nil {
		return err
	}
	held, _ := ctx.Value(heldKey{s.request.Name}).(*heldLock)
	if held == nil || held != s.op || o.creation == nil || o.creation.op.Load() != s.op || o.stage != s.stage {
		return hostStateRemovalError("the provider source capture changed its original OP or stage")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.creation.started || o.creation.bound || o.creation.sourceScope != s || o.creation.request != s.request {
		return hostStateRemovalError("the original provider source capture phase ended or changed")
	}
	return ctx.Err()
}

func checkHostMachineSourceObserverContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if ctx.Value(hostMembershipContextKey{}) != nil || ctx.Value(hostMutationContextKey{}) != nil || ctx.Value(hostForgetMembershipKey{}) != nil || ctx.Value(hostStateInspectionKey{}) != nil || ctx.Value(hostTerminalInspectionKey{}) != nil {
		return hostStateRemovalError("a machine-source observer cannot run inside membership or local mutation/inspection")
	}
	return nil
}
