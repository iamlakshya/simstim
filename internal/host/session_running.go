package host

// SessionRunning uses the canonical holder/unit/ownership and pending-start check.
func SessionRunning(m *Manifest) bool { return m != nil && holderPendingOrAlive(m.ID, m) }
