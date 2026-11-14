package client

import (
	"context"
	"reflect"
)

type hostMachineSourceBirth struct {
	scope    *HostCreationSourceScope
	result   Host
	bound    bool
	consumed bool
}

// RecordHostMachineSourceBirthContext records the actual builtin's closed
// birth observation, never caller facts. Its key comes from the protected
// reservation. The fixed observer requires its private original Create frame.
// An externally created resource remains the caller's responsibility on error.
func RecordHostMachineSourceBirthContext(ctx context.Context, result Host) (Host, error) {
	if err := checkHostMachineSourceObserverContext(ctx); err != nil {
		return result, err
	}
	o, _ := ctx.Value(hostStateObservationKey{}).(*HostStateObservation)
	if o == nil || o.role != hostStateCreation || o.creation == nil {
		return result, hostStateRemovalError("the source result has no actual closed creation reservation")
	}
	o.mu.Lock()
	scope, entered := o.creation.sourceScope, o.creation.sourceProducerEntered
	o.mu.Unlock()
	if scope == nil || !entered {
		return result, hostStateRemovalError("the source result did not enter its original closed provider frame")
	}
	if err := scope.CheckContext(ctx); err != nil {
		return result, err
	}
	observer, err := hostMachineSourceObserver(scope.request.Provider)
	if err != nil {
		return result, err
	}
	if result.machineSourceBirth != nil || result.Name != scope.request.Name || result.Machine == nil || result.Machine.Provider != scope.request.Provider || result.Machine.ID == "" {
		return result, hostStateRemovalError("the actual source result differs from its protected provider request")
	}
	o.mu.Lock()
	if o.creation.sourceRecordAttempted || o.creation.sourceBirth != nil || o.creation.bound {
		o.mu.Unlock()
		return result, hostStateRemovalError("this provider creation already attempted its one birth observation")
	}
	o.creation.sourceRecordAttempted = true
	o.mu.Unlock()
	// Freeze the caller's complete data before invoking the native observer.
	result = cloneObservationHost(result)
	observed, err := observer(ctx, scope, cloneObservationHost(result))
	if err != nil {
		return result, err
	}
	observed, err = NormaliseMachineSource(scope.request.Provider, observed)
	if err != nil {
		return result, err
	}
	if result.Machine.Source != nil && !reflect.DeepEqual(result.Machine.Source, observed) {
		return result, hostStateRemovalError("the result DTO differs from the builtin's actual birth observation")
	}
	if err := scope.CheckContext(ctx); err != nil {
		return result, err
	}
	result.Machine.Source = CloneMachineSource(observed)
	err = withHostMembershipContext(ctx, func(locked context.Context) error {
		if err := scope.checkLocked(locked); err != nil {
			return err
		}
		o.mu.Lock()
		defer o.mu.Unlock()
		if o.creation.sourceBirth != nil || o.creation.bound {
			return hostStateRemovalError("the provider birth observation was already bound or replayed")
		}
		birth := &hostMachineSourceBirth{scope: scope, result: cloneObservationHost(result)}
		o.creation.sourceBirth = birth
		result.machineSourceBirth = birth
		return nil
	})
	return result, err
}

// The containing observation mutex is held by the one creation binder.
func checkHostMachineSourceBirthBindingLocked(o *HostStateObservation, result Host) error {
	birth := o.creation.sourceBirth
	if result.Machine == nil || result.Machine.Source == nil {
		if birth != nil || result.machineSourceBirth != nil {
			return hostStateRemovalError("the creation result discarded its recorded machine source")
		}
		return nil
	}
	if birth == nil || birth.consumed || birth.scope != o.creation.sourceScope || result.machineSourceBirth != birth {
		return hostStateRemovalError("machine source data has no original closed builtin birth token")
	}
	original, proposed := cloneObservationHost(birth.result), cloneObservationHost(result)
	original.Machine.Created, proposed.Machine.Created = "", ""
	if !SameEntry(original, proposed) {
		return hostStateRemovalError("the provider result changed after its actual source birth observation")
	}
	return nil
}

func bindHostMachineSourceBirthLocked(o *HostStateObservation, result Host) {
	if birth := o.creation.sourceBirth; birth != nil {
		birth.result = cloneObservationHost(result)
		birth.bound = true
	}
}

func checkHostMachineSourceBirthPublication(ctx context.Context, change HostChange) error {
	o, _ := ctx.Value(hostStateObservationKey{}).(*HostStateObservation)
	if o == nil || o.role != hostStateCreation || o.creation == nil || change.Was != nil || change.Now == nil || change.Now.Name != o.name || change.Now.Machine == nil {
		return hostStateRemovalError("a new machine source requires its original closed creation commit")
	}
	if err := o.checkMembership(ctx); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	birth := o.creation.sourceBirth
	if birth == nil || !birth.bound || birth.consumed || !o.creation.bound || birth.scope != o.creation.sourceScope || change.Now.machineSourceBirth != birth ||
		!SameMachine(birth.result.Machine, change.Now.Machine) || !reflect.DeepEqual(birth.result.Machine.Source, change.Now.Machine.Source) {
		return hostStateRemovalError("the machine-source birth token is missing, changed or already consumed")
	}
	return ctx.Err()
}

func recordHostMachineSourceBirthCommit(ctx context.Context, change HostChange) {
	o, _ := ctx.Value(hostStateObservationKey{}).(*HostStateObservation)
	if o == nil || o.role != hostStateCreation || o.creation == nil || change.Was != nil || change.Now == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if birth := o.creation.sourceBirth; birth != nil && change.Now.machineSourceBirth == birth {
		birth.consumed = true
	}
}
