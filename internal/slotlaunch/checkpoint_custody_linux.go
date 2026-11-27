//go:build linux

package slotlaunch

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
)

// CheckpointCustodyV1 retains copies of an actual original slot source's
// root-issued native roles. Only that live NativeSourceV1 can create it. A
// request, saved allocation record or list of descriptor numbers cannot.
// It grants neither execution nor a new allocation, and holds no slot lock.
type CheckpointCustodyV1 struct {
	mu          sync.Mutex
	operations  sync.Mutex
	active      sync.WaitGroup
	closing     bool
	loanSent    bool
	once        sync.Once
	closeErr    error
	original    *NativeSourceV1
	source      *NativeSourceV1
	vmPeer      *os.File
	guest       *os.File
	checkpoints *os.File
	ctx         context.Context
	cancel      context.CancelFunc
}

// RetainCheckpointCustodyV1 must run before the same original slot is stopped.
// The copies keep its actual birth handles, disk and cgroup available after
// NativeSourceV1.Close. The caller owns this custody until every disk operation
// has joined and explicitly closes it; stopping the VM does not close it.
func (s *NativeSourceV1) RetainCheckpointCustodyV1(ctx context.Context) (result *CheckpointCustodyV1, returned error) {
	if s == nil || ctx == nil {
		return nil, refused("the checkpoint has no original live slot source")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(ctx, s.request); err != nil {
		return nil, err
	}
	guest, ok := s.guest.(*sourceGuestV1)
	if !ok || guest == nil || guest.source != s || s.guestPeer == nil || !pidLive(s.guestPeer) {
		return nil, refused("the checkpoint source has no original native VMM guest")
	}
	lifetime, cancel := context.WithCancel(ctx)
	retained := &CheckpointCustodyV1{original: s, ctx: lifetime, cancel: cancel}
	retained.source = &NativeSourceV1{request: s.request, owner: lifetime, identity: s.identity, boot: s.boot, adopted: true}
	complete := false
	defer func() {
		if !complete {
			if closeErr := retained.Close(); closeErr != nil {
				result = retained
				returned = errors.Join(returned, closeErr)
			}
		}
	}()
	for _, file := range s.files {
		copy, err := dupFile(file)
		if err != nil {
			return nil, err
		}
		retained.source.files = append(retained.source.files, copy)
	}
	var err error
	retained.source.parent, err = dupFile(s.parent)
	if err != nil {
		return nil, err
	}
	retained.vmPeer, err = dupFile(s.guestPeer)
	if err != nil {
		return nil, err
	}
	local, ok := guest.Conn.(*net.UnixConn)
	if !ok {
		return nil, refused("the original checkpoint guest has no native Unix endpoint")
	}
	retained.guest, err = local.File()
	if err != nil {
		return nil, err
	}
	if err := errors.Join(s.check(ctx, s.request), retained.source.check(ctx, s.request)); err != nil {
		return nil, err
	}
	if !pidLive(retained.vmPeer) {
		return nil, refused("the original VMM ended during checkpoint custody capture")
	}
	complete = true
	return retained, nil
}

func (custody *CheckpointCustodyV1) begin(ctx context.Context) (func(), error) {
	if custody == nil || ctx == nil {
		return nil, refused("the original checkpoint custody is absent")
	}
	custody.mu.Lock()
	defer custody.mu.Unlock()
	if custody.closing || custody.original == nil || custody.source == nil || custody.ctx == nil || custody.ctx.Err() != nil || ctx.Err() != nil {
		return nil, refused("the original checkpoint custody ended")
	}
	custody.active.Add(1)
	return custody.active.Done, nil
}

func (custody *CheckpointCustodyV1) checkRunning(ctx context.Context) error {
	if custody == nil || custody.source == nil || custody.original == nil || custody.vmPeer == nil {
		return refused("the original checkpoint source is unavailable")
	}
	if err := custody.source.check(ctx, custody.source.request); err != nil {
		return err
	}
	if !pidLive(custody.vmPeer) {
		return refused("the original checkpoint VMM peer ended")
	}
	return nil
}

// WithLoanFilesV1 borrows this actual live producer's fixed seventeen native
// roles. The callback may send them once on the same authenticated outside
// creator lane; it must not close or retain these local handles. The receiver
// still needs its genuine live sender/creation/namespace/cgroup and SAME OP
// factory. Copied descriptors or this role count are not that factory.
func (custody *CheckpointCustodyV1) WithLoanFilesV1(ctx context.Context, send func([]*os.File) error) error {
	if send == nil {
		return refused("the actual checkpoint native loan has no sender")
	}
	// Capture the root-owned directory from the SAME live root source before
	// offering any outside loan. A fixed session string cannot supply it.
	if _, files, err := custody.callRunning(ctx, "list", "", 0, nil); err != nil {
		return err
	} else {
		for _, file := range files {
			file.Close()
		}
	}
	done, err := custody.begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	custody.operations.Lock()
	defer custody.operations.Unlock()
	if err := custody.checkRunning(ctx); err != nil {
		return err
	}
	if len(custody.source.files) != 13 || custody.source.parent == nil || custody.vmPeer == nil || custody.guest == nil || custody.checkpoints == nil {
		return refused("the original checkpoint native loan has incomplete roles")
	}
	if custody.loanSent {
		return refused("the original checkpoint custody loan was already consumed")
	}
	roles := append([]*os.File(nil), custody.source.files...)
	roles = append(roles, custody.source.parent, custody.vmPeer, custody.guest, custody.checkpoints)
	custody.loanSent = true // A partial native send may already have loaned roles.
	result := send(roles)
	return errors.Join(result, custody.checkRunning(ctx))
}

func (custody *CheckpointCustodyV1) clone(ctx context.Context) (result *CheckpointCustodyV1, returned error) {
	done, err := custody.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	custody.operations.Lock()
	defer custody.operations.Unlock()
	if err := custody.checkRunning(ctx); err != nil {
		return nil, err
	}
	// This private copy is a selected read-cap keeper. It survives the
	// running borrow's cancellation; it cannot be obtained through a DTO or
	// used for a new running backend by callers of SnapshotV1.
	lifetime, cancel := context.WithCancel(context.WithoutCancel(ctx))
	copy := &CheckpointCustodyV1{original: custody.original, ctx: lifetime, cancel: cancel}
	source := custody.source
	copy.source = &NativeSourceV1{request: source.request, owner: lifetime, identity: source.identity, boot: source.boot, adopted: true}
	complete := false
	defer func() {
		if !complete {
			if closeErr := copy.Close(); closeErr != nil {
				result = copy
				returned = errors.Join(returned, closeErr)
			}
		}
	}()
	for _, file := range source.files {
		duplicate, err := dupFile(file)
		if err != nil {
			return nil, err
		}
		copy.source.files = append(copy.source.files, duplicate)
	}
	copy.source.parent, err = dupFile(source.parent)
	if err != nil {
		return nil, err
	}
	copy.vmPeer, err = dupFile(custody.vmPeer)
	if err != nil {
		return nil, err
	}
	copy.guest, err = dupFile(custody.guest)
	if err != nil {
		return nil, err
	}
	if custody.checkpoints != nil {
		copy.checkpoints, err = dupFile(custody.checkpoints)
		if err != nil {
			return nil, err
		}
	}
	if err := errors.Join(custody.checkRunning(ctx), copy.checkRunning(ctx)); err != nil {
		return nil, err
	}
	complete = true
	return copy, nil
}

// Close cancels and joins only this custody's operations, then closes its own
// duplicated native roles. It never stops the original VM or closes its source.
func (custody *CheckpointCustodyV1) Close() error {
	if custody == nil {
		return nil
	}
	custody.once.Do(func() {
		custody.mu.Lock()
		custody.closing = true
		if custody.cancel != nil {
			custody.cancel()
		}
		custody.mu.Unlock()
		custody.active.Wait()
		if custody.source != nil {
			for _, file := range custody.source.files {
				custody.closeErr = errors.Join(custody.closeErr, file.Close())
			}
			if custody.source.parent != nil {
				custody.closeErr = errors.Join(custody.closeErr, custody.source.parent.Close())
			}
		}
		if custody.vmPeer != nil {
			custody.closeErr = errors.Join(custody.closeErr, custody.vmPeer.Close())
		}
		if custody.guest != nil {
			custody.closeErr = errors.Join(custody.closeErr, custody.guest.Close())
		}
		if custody.checkpoints != nil {
			custody.closeErr = errors.Join(custody.closeErr, custody.checkpoints.Close())
		}
	})
	return custody.closeErr
}
