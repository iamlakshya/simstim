package client

import (
	"context"
	"time"
)

// DestroyPurgedHost always asks the provider again. A previous receipt never
// turns a provider ID or a saved boolean into evidence that a machine is gone.
func DestroyPurgedHost(ctx context.Context) error {
	p, err := purgeOperationOfV1(ctx)
	if err != nil {
		return err
	}
	if err := p.check(ctx, p.conn); err != nil {
		return err
	}
	p.mu.Lock()
	record := p.receipt
	p.mu.Unlock()
	if !record.complete() || record.Original.Machine == nil {
		return grantOperationError()
	}
	machine, err := OpenMachine(record.Original)
	if err != nil {
		return err
	}
	state, err := machine.Status(ctx)
	if err != nil {
		return err
	}
	if err := p.check(ctx, p.conn); err != nil {
		return err
	}
	if state != MachineMissing {
		owned, cancel := context.WithTimeout(ctx, 5*time.Minute)
		err = machine.Destroy(owned)
		cancel()
		if err != nil {
			return err
		}
		state, err = machine.Status(ctx)
		if err != nil {
			return err
		}
		if state != MachineMissing {
			return &UserError{Msg: "the provider has not confirmed that the original machine is gone", Hint: "Inspect it with the provider's own tools, then resume the same removal.", Code: "destroy_unconfirmed", Kind: KindRefused}
		}
	}
	return ctx.Err()
}
