package egress

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestRegistryRotationCanJoinACancelledQueuedMintWithoutUnlocking(t *testing.T) {
	var gate registryGate
	gate.Lock() // an admitted rotation owns the complete mutation transaction
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered, finished := make(chan struct{}), make(chan error, 1)
	go func() {
		close(entered)
		err := gate.LockContext(ctx)
		if err == nil {
			gate.Unlock()
		}
		finished <- err
	}()
	<-entered
	// Closing the affected request must let its pending mint callback leave
	// while the rotation still owns mutation. A plain mutex deadlocks here.
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("a cancelled request published or lost its cancellation cause")
		}
	case <-time.After(2 * time.Second):
		gate.Unlock()
		t.Fatal("rotation could not join its cancelled queued response callback")
	}
	// A cancelled waiter must not release the rotation's ownership token.
	gate.Unlock()
	if err := gate.LockContext(t.Context()); err != nil {
		t.Fatal("completed cancellation poisoned later registry mutations")
	}
	gate.Unlock()
}

func TestRegistryGateKeepsControlAndResponsePublicationsSerial(t *testing.T) {
	var gate registryGate
	value := 0
	var workers sync.WaitGroup
	for range 32 {
		workers.Go(func() {
			for range 16 {
				if err := gate.LockContext(t.Context()); err != nil {
					t.Error("live registry admission unexpectedly failed")
					return
				}
				value++
				gate.Unlock()
			}
		})
	}
	workers.Wait()
	if value != 32*16 {
		t.Fatal("registry publications overlapped or were silently dropped")
	}
}

func TestCancelledRegistryAdmissionCannotAcquireOrReleaseAnotherOwner(t *testing.T) {
	var gate registryGate
	gate.Lock()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := gate.LockContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("an already cancelled registry mutation acquired authority")
	}
	if err := gate.LockContext(nil); err == nil {
		t.Fatal("registry admission accepted a missing lifetime")
	}
	gate.Unlock()
	gate.Lock()
	gate.Unlock()
}
