package nativememory

import (
	"context"
	"errors"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestZeroNativeMemoryFactsNeverBecomeCapacity(t *testing.T) {
	var current Current
	if _, err := current.Observe(t.Context()); err == nil {
		t.Fatal("zero current scope became a provider")
	}
	if err := current.MatchHierarchyRoot(t.Context(), nil); err == nil {
		t.Fatal("a nil root FD became boundary proof")
	}
	if _, err := (Sample{}).PhysicalAvailable(); err == nil {
		t.Fatal("zero sample became known physical zero")
	}
	if _, err := (Sample{}).VisibleAdditionalHeadroom(); err == nil {
		t.Fatal("zero sample became unlimited headroom")
	}
}

// This private read-lifetime double tests joining only. It is not reachable
// through CaptureCurrent and supplies no native or reservation evidence.
type blockedMemoryRead struct {
	entered, release, closed chan struct{}
	closeErr                 error
}

func (b *blockedMemoryRead) observe(ctx context.Context) (sampleData, error) {
	close(b.entered)
	<-b.release
	return sampleData{physical: 20, ancestor: Limit{kind: Finite, bytes: 10}}, ctx.Err()
}
func (*blockedMemoryRead) check(context.Context) error                 { return nil }
func (*blockedMemoryRead) match(context.Context, *os.File, bool) error { return ErrUnknown }
func (b *blockedMemoryRead) close() error                              { close(b.closed); return b.closeErr }

func waitMemorySignal(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("owned memory read fixture did not join")
	}
}

func TestNativeMemoryCloseJoinsReadAndPreservesCloseFailure(t *testing.T) {
	owner, cancel := context.WithCancel(t.Context())
	marker := errors.New("owned accounting descriptor close failed")
	backend := &blockedMemoryRead{entered: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{}), closeErr: marker}
	current := &Current{owner: owner, backend: backend}
	unblock := sync.OnceFunc(func() { close(backend.release) })
	readDone, closeDone := make(chan struct{}), make(chan struct{})
	var readErr, closeErr error
	var closeStarted bool
	t.Cleanup(func() {
		cancel()
		unblock()
		waitMemorySignal(t, readDone)
		if closeStarted {
			waitMemorySignal(t, closeDone)
		} else {
			_ = current.Close()
		}
	})
	go func() { _, readErr = current.Observe(t.Context()); close(readDone) }()
	waitMemorySignal(t, backend.entered)
	closeStarted = true
	go func() { closeErr = current.Close(); close(closeDone) }()
	for deadline := time.Now().Add(5 * time.Second); ; {
		if !current.mu.TryRLock() {
			break // Close has reached its exclusive gate behind this read.
		}
		current.mu.RUnlock()
		if time.Now().After(deadline) {
			t.Fatal("close never entered its native read-drain gate")
		}
		runtime.Gosched()
	}
	select {
	case <-backend.closed:
		t.Fatal("close reused a descriptor while its owned read was active")
	default:
	}
	unblock()
	waitMemorySignal(t, readDone)
	waitMemorySignal(t, closeDone)
	if readErr != nil || !errors.Is(closeErr, marker) || !errors.Is(current.Close(), marker) {
		t.Fatalf("joined read/close results = %v, %v", readErr, closeErr)
	}
	if _, err := current.Observe(t.Context()); err == nil {
		t.Fatal("closed accounting owner became a provider again")
	}
}
