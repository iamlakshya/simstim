//go:build linux

package slotlaunch

import (
	"context"
	"errors"
	"os"
	"sync"
)

// This private owner originates ONLY in the live actual keeper inspector.
// It retains the same EX OFD locally so runtime stop can use the distinct
// reserved operation while the outside eighteen-role copy holds the slot.
type originalAllocationLeaseV2 struct {
	mu      sync.Mutex
	active  sync.WaitGroup
	closing bool
	custody *CheckpointCustodyV1
	lease   *os.File
	capture *CheckpointOperationOwnerV1
	once    sync.Once
	err     error
}

type checkpointReservationLoanV2 struct {
	mu             sync.Mutex
	allocation     *originalAllocationLeaseV2
	lease          *os.File
	sent, closed   bool
	ownsAllocation bool
	closeErr       error
}

// CaptureReservationLoanV2 consumes only this genuine live custody's root
// inspector, never a descriptor vector or saved slot record. The returned
// loan owns its local DUP; NativeSource keeps a separate actual reservation
// owner until its real old runtime closes. Outside receipt still needs its
// authenticated original sender, SAME OP and genuine stopped owner.
func (custody *CheckpointCustodyV1) CaptureReservationLoanV2(ctx context.Context) (result *checkpointReservationLoanV2, returned error) {
	if _, files, err := custody.callRunning(ctx, "list", "", 0, nil); err != nil {
		return nil, err
	} else {
		for _, file := range files {
			file.Close()
		}
	}
	retained, err := custody.clone(ctx)
	if err != nil {
		if retained != nil {
			return &checkpointReservationLoanV2{allocation: &originalAllocationLeaseV2{custody: retained}, ownsAllocation: true}, err
		}
		return nil, err
	}
	allocation := &originalAllocationLeaseV2{custody: retained}
	complete := false
	defer func() {
		if !complete {
			if closeErr := allocation.Close(); closeErr != nil {
				result = &checkpointReservationLoanV2{allocation: allocation, ownsAllocation: true}
				returned = errors.Join(returned, closeErr)
			}
		}
	}()
	roles := allocation.roles()
	operation, err := RunCheckpointOperationV1(ctx, roles, CheckpointOperationV1{Session: retained.source.request.Session, Action: "reserve"})
	allocation.capture = operation
	if operation != nil {
		readErr := operation.WithFilesV1(context.WithoutCancel(ctx), func(outputs []*os.File) error {
			if len(outputs) != 1 {
				return refused("the actual keeper reservation has another output role vector")
			}
			if err := CheckOriginalReservationLeaseV2(outputs[0]); err != nil {
				return err
			}
			var err error
			allocation.lease, err = dupFile(outputs[0])
			return err
		})
		err = errors.Join(err, readErr)
	}
	if err != nil {
		return nil, err
	}
	if allocation.lease == nil {
		return nil, refused("the actual root inspector returned no original slot lease")
	}
	if err := operation.Close(); err != nil {
		return nil, err
	}
	allocation.capture = nil
	original := custody.original
	original.mu.Lock()
	if err := original.check(ctx, original.request); err != nil {
		original.mu.Unlock()
		return nil, err
	}
	if original.reserved == nil {
		original.reserved = allocation
		complete = true
	} else {
		existing := original.reserved
		original.mu.Unlock()
		if err := allocation.Close(); err != nil {
			return nil, err
		}
		allocation = existing
		complete = true
		original.mu.Lock()
	}
	original.mu.Unlock()
	allocation.mu.Lock()
	defer allocation.mu.Unlock()
	if allocation.closing || allocation.lease == nil {
		return nil, refused("the original source reservation is closing")
	}
	copy, err := dupFile(allocation.lease)
	if err != nil {
		return nil, err
	}
	return &checkpointReservationLoanV2{allocation: allocation, lease: copy}, nil
}

func (allocation *originalAllocationLeaseV2) roles() []*os.File {
	c := allocation.custody
	roles := append([]*os.File(nil), c.source.files...)
	return append(roles, c.source.parent, c.vmPeer, c.guest, c.checkpoints)
}

func (allocation *originalAllocationLeaseV2) stop(ctx context.Context) error {
	allocation.mu.Lock()
	if allocation.closing || allocation.lease == nil {
		allocation.mu.Unlock()
		return refused("the original reserved stop owner is closing")
	}
	allocation.active.Add(1)
	roles := append(allocation.roles(), allocation.lease)
	allocation.mu.Unlock()
	defer allocation.active.Done()
	if err := CheckOriginalReservationLeaseV2(allocation.lease); err != nil {
		return err
	}
	operation, err := RunReservedCheckpointOperationV2(ctx, roles, CheckpointOperationV1{Session: allocation.custody.source.request.Session, Action: "stop"})
	if operation != nil {
		err = errors.Join(err, operation.Close())
	}
	return err
}

func (allocation *originalAllocationLeaseV2) Close() error {
	if allocation == nil {
		return nil
	}
	allocation.once.Do(func() {
		allocation.mu.Lock()
		allocation.closing = true
		allocation.mu.Unlock()
		allocation.active.Wait()
		if allocation.capture != nil {
			allocation.err = errors.Join(allocation.err, allocation.capture.Close())
		}
		if allocation.lease != nil {
			allocation.err = errors.Join(allocation.err, allocation.lease.Close())
		}
		if allocation.custody != nil {
			allocation.err = errors.Join(allocation.err, allocation.custody.Close())
		}
	})
	return allocation.err
}

func (loan *checkpointReservationLoanV2) WithLoanFilesV2(ctx context.Context, send func([]*os.File) error) error {
	if loan == nil || ctx == nil || send == nil {
		return refused("the reserved native loan has no actual sender")
	}
	loan.mu.Lock()
	defer loan.mu.Unlock()
	if loan.closed || loan.sent || loan.allocation == nil || loan.lease == nil {
		return refused("the original reserved native loan ended or was consumed")
	}
	a := loan.allocation
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closing {
		return refused("the original reserved allocation is closing")
	}
	if err := errors.Join(a.custody.checkRunning(ctx), CheckOriginalReservationLeaseV2(loan.lease)); err != nil {
		return err
	}
	roles := append(a.roles(), loan.lease)
	loan.sent = true
	result := send(roles)
	return errors.Join(result, a.custody.checkRunning(ctx), CheckOriginalReservationLeaseV2(loan.lease))
}

func (loan *checkpointReservationLoanV2) Close() error {
	if loan == nil {
		return nil
	}
	loan.mu.Lock()
	defer loan.mu.Unlock()
	if loan.closed {
		return loan.closeErr
	}
	loan.closed = true
	if loan.lease != nil {
		loan.closeErr = errors.Join(loan.closeErr, loan.lease.Close())
	}
	if loan.ownsAllocation && loan.allocation != nil {
		loan.closeErr = errors.Join(loan.closeErr, loan.allocation.Close())
	}
	return loan.closeErr
}
