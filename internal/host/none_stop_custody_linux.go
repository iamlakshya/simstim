//go:build linux

package host

// Bind before any stop prepare or commit. This is the actual original source,
// never a record, completion byte, PID or source reconstructed at helper exit.
func (source *StoppedNoneSessionV1) bindCustodyV1() error {
	if source == nil || source.self != source || source.operation == nil {
		return sessionLeaseError("the original None stop has no actual custody owner")
	}
	noneCustodyV1.mu.Lock()
	defer noneCustodyV1.mu.Unlock()
	operation := source.operation
	operation.gate.Lock()
	defer operation.gate.Unlock()
	if noneCustodyV1.closed || !operation.active.Load() || operation.noneStop != nil {
		return sessionLeaseError("the original None stop admission or operation already ended")
	}
	if noneCustodyV1.stops == nil {
		noneCustodyV1.stops = map[*StoppedNoneSessionV1]struct{}{}
	}
	noneCustodyV1.stops[source] = struct{}{}
	operation.noneStop = source
	noneCustodyV1.changedV1()
	return nil
}

// Only its actual successful close retires a registered stop and its OP pin.
func (source *StoppedNoneSessionV1) releaseCustodyV1() {
	noneCustodyV1.mu.Lock()
	defer noneCustodyV1.mu.Unlock()
	if _, exists := noneCustodyV1.stops[source]; !exists {
		return
	}
	operation := source.operation
	operation.gate.Lock()
	if operation.noneStop == source {
		operation.noneStop = nil
	}
	operation.gate.Unlock()
	delete(noneCustodyV1.stops, source)
	noneCustodyV1.changedV1()
}
