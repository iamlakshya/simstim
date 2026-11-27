//go:build linux

package host

// The real original outside prepare issuer selects resumability only after
// its native hello has bound this line to that exact unreaped creation. The
// marker supplies no operation, native audience or cleanup acknowledgement.
func (launch *startupLaunchV1) enableSandboxPrepareCursorV1() error {
	if launch == nil || launch.operation == nil || !launch.operation.IsSandboxPrepareV1() ||
		launch.operation.startup != launch || launch.line == nil || launch.nativePeer() == nil || launch.nativePeer().identity == nil {
		return sessionLeaseError("the resumable preparation reader has no original private native issuer")
	}
	line := launch.line
	line.readMu.Lock()
	defer line.readMu.Unlock()
	line.mu.Lock()
	defer line.mu.Unlock()
	if line.closed || line.reading || len(line.files) != 0 || line.nativePeer == nil || line.prepareCursor != nil || line.preparePoison {
		return sessionLeaseError("the original preparation reader changed before its private cursor was selected")
	}
	line.prepareCursor = &sandboxPrepareFrameCursorV1{}
	return nil
}
