package host

import (
	"bytes"
	"context"
	"sync"
	"time"
)

// AutomaticBootOperationV1 is issued only from the closed automatic boot
// entry after validating the protected restore plan under the primary lease.
// A generic session operation, client boot or running join cannot mint it.
type AutomaticBootOperationV1 struct {
	operation *SessionOperation
	plan      []byte
	mu        sync.Mutex
	used      bool
}

// BeginAutomaticBootOperation retains the existing primary operation lease
// and validates the exact complete automatic plan before issuing provenance.
func BeginAutomaticBootOperation(ctx context.Context, id string, wait time.Duration) (*SessionOperation, error) {
	// Only the real outside controller may issue this ordinary owned scope.
	// A child inside a session must consume its original verified OP loan;
	// loading a saved automatic plan or setting environment is not issuance.
	if err := CheckCaller(); err != nil {
		return nil, err
	}
	operation, err := BeginSessionOperation(ctx, id, wait)
	if err != nil {
		return nil, err
	}
	if err := prepareAutomaticBootOperationV1(operation); err != nil {
		operation.Close()
		return nil, err
	}
	return operation, nil
}

// AutomaticBoot returns this operation's opaque automatic-boot provenance.
// Nil identifies every ordinary client boot and every generic operation.
func (o *SessionOperation) AutomaticBoot() *AutomaticBootOperationV1 {
	if o == nil || !o.Valid(o.id) {
		return nil
	}
	return o.automaticBoot
}

// WithCurrent consumes one separately authorised automatic boot refresh.
// The producer must retain its authenticated receipt and disk lease through
// this callback and the canonical durable publication. No saved row can issue
// this token. Failed or cancelled publication cannot reuse it for a later boot.
func (b *AutomaticBootOperationV1) WithCurrent(ctx context.Context, publish func(*Manifest) error) error {
	if b == nil || b.operation == nil || publish == nil {
		return automaticRestoreError()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.used || !b.operation.Valid(b.operation.id) {
		return automaticRestoreError()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m, err := b.operation.Load()
	if err != nil {
		return err
	}
	if !bytes.Equal(m.AutomaticRestore, b.plan) {
		return automaticRestoreError()
	}
	if _, err := ValidateAutomaticRestore(m); err != nil {
		return err
	}
	b.used = true
	return publish(m)
}
