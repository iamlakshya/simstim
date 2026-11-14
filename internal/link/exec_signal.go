package link

// ValidExecSignal exposes the ONE existing owned-command signal set to the
// VM PTY transport. It grants no process handle or PID lookup capability.
func ValidExecSignal(number uint32) bool { return validExecSignal(number) }
