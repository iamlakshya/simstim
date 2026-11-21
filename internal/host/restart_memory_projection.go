package host

func applyAdmittedRestartMemoryV1(operation *SessionOperation, m *Manifest) error {
	if operation == nil || m == nil || !operation.Valid(m.ID) {
		return sessionLeaseError("restart memory has no original current operation")
	}
	operation.gate.Lock()
	defer operation.gate.Unlock()
	receipt := operation.restartMemory
	if receipt == nil || receipt.operation != operation || receipt.used || !operation.active.Load() {
		return sessionLeaseError("the restart has no unconsumed original memory admission")
	}
	m.Limits = receipt.after
	return nil
}
