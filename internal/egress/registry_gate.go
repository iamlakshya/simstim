package egress

import (
	"context"
	"sync"
)

// registryGate serialises the one canonical registry. Response callbacks
// must be able to leave when a rotation cancels and joins their request.
// It stores no secret, generation, authority or second registry.
type registryGate struct {
	once  sync.Once
	token chan struct{}
}

func (gate *registryGate) Lock() {
	if err := gate.LockContext(context.Background()); err != nil {
		panic("background registry ownership failed")
	}
}

func (gate *registryGate) LockContext(ctx context.Context) error {
	if ctx == nil {
		return memberError("bad_request", "registry mutation has no context", "Retry the operation with matching helpers.")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	gate.once.Do(func() { gate.token = make(chan struct{}, 1) })
	select {
	case gate.token <- struct{}{}:
		// Cancellation can race the select. Relinquish only the token this
		// call just acquired before returning a failed mutation admission.
		if err := ctx.Err(); err != nil {
			<-gate.token
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (gate *registryGate) Unlock() {
	select {
	case <-gate.token:
	default:
		panic("unlock of unowned registry gate")
	}
}
