package host

import "sync"

// This registry contains only actual same-process Delete workers and original
// N5 stop objects. Nothing is reloaded from saved names or descriptive DATA.
// Admission and the helper-exit barrier share this exact mutex.
type noneCustodyRegistryV1 struct {
	mu      sync.Mutex
	closed  bool
	changed chan struct{}
	deletes map[string]*noneDeleteRetainedV1
	stops   map[*StoppedNoneSessionV1]struct{}
}

var noneCustodyV1 noneCustodyRegistryV1

func (registry *noneCustodyRegistryV1) changedV1() {
	if registry.changed != nil {
		close(registry.changed)
	}
	registry.changed = make(chan struct{})
}

// The returned worker is locked and registered before it can acquire an OP.
// A retry may take its SAME retained worker; closed admission creates none.
func (registry *noneCustodyRegistryV1) admitDeleteV1(id string) (*noneDeleteRetainedV1, error) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if retained := registry.deletes[id]; retained != nil {
		if !retained.mu.TryLock() {
			return nil, UserError("busy", "the original None deletion is still active", "Wait for that exact deletion transaction to finish, then retry.")
		}
		return retained, nil
	}
	if registry.closed {
		return nil, sessionLeaseError("the original helper ended admission of new None deletion owners")
	}
	retained := &noneDeleteRetainedV1{}
	retained.mu.Lock()
	if registry.deletes == nil {
		registry.deletes = map[string]*noneDeleteRetainedV1{}
	}
	registry.deletes[id] = retained
	registry.changedV1()
	return retained, nil
}

func (registry *noneCustodyRegistryV1) finishDeleteV1(id string, retained *noneDeleteRetainedV1) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.deletes[id] == retained {
		delete(registry.deletes, id)
		registry.changedV1()
	}
}

func hasRetainedNoneDeleteV1(id string) bool {
	noneCustodyV1.mu.Lock()
	defer noneCustodyV1.mu.Unlock()
	return noneCustodyV1.deletes[id] != nil
}

// WaitForNoneCustodyV1 closes only new admission and joins the actual owners.
// Logical request completion, an idle deadline and EOF cannot retire them.
// The normal command boundary and the bridge's idle exit both use this join.
func WaitForNoneCustodyV1() { noneCustodyV1.joinV1() }

func (registry *noneCustodyRegistryV1) joinV1() {
	registry.mu.Lock()
	if !registry.closed {
		registry.closed = true
		registry.changedV1()
	}
	for len(registry.deletes) != 0 || len(registry.stops) != 0 {
		if registry.changed == nil {
			registry.changed = make(chan struct{})
		}
		changed := registry.changed
		registry.mu.Unlock()
		<-changed
		registry.mu.Lock()
	}
	registry.mu.Unlock()
}
