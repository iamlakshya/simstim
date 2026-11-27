package host

import (
	"bytes"
	"errors"
)

// prepareAutomaticBootOperationV1 belongs only to the outside restore
// controller after its selected plan's strong Save under this SAME native OP.
// It rereads the protected primary; no raw Manifest, callback, environment or
// saved-row caller can issue an automatic token inside a running session.
func prepareAutomaticBootOperationV1(operation *SessionOperation) error {
	if err := CheckCaller(); err != nil {
		return err
	}
	if operation == nil || operation.native == nil || operation.native.borrowed || operation.ownerContext == nil || !operation.Valid(operation.Primary()) {
		return automaticRestoreError()
	}
	if err := operation.ownerContext.Err(); err != nil {
		return err
	}
	current, err := operation.LoadContext(operation.ownerContext)
	if err != nil {
		return err
	}
	if current.ID != operation.Primary() || current.SandboxOf != "" || current.Wall == nil {
		return automaticRestoreError()
	}
	if _, err := ValidateAutomaticRestore(current); err != nil {
		return err
	}
	operation.gate.Lock()
	defer operation.gate.Unlock()
	if !operation.active.Load() || operation.startup != nil || operation.automaticBoot != nil {
		return automaticRestoreError()
	}
	if err := errors.Join(operation.native.check(operation.ownerContext), operation.ownerContext.Err()); err != nil {
		return err
	}
	operation.automaticBoot = &AutomaticBootOperationV1{operation: operation, plan: bytes.Clone(current.AutomaticRestore)}
	return nil
}
