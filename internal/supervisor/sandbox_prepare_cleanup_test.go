package supervisor

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// This adapter oracle tests the SAME returned owner and its real retry order.
// Concrete runtime/native destruction has separate Linux tests.
type prepareCleanupRuntimeV1 struct {
	kill, wait, destroy atomic.Int64
	release             chan struct{}
	entered             chan struct{}
}

func (*prepareCleanupRuntimeV1) Start(context.Context) (net.Conn, error) {
	return nil, errors.New("cleanup-only fixture")
}
func (r *prepareCleanupRuntimeV1) Kill() error {
	r.kill.Add(1)
	return errors.New("kill diagnostic")
}
func (r *prepareCleanupRuntimeV1) Wait() error {
	r.wait.Add(1)
	return errors.New("exit diagnostic")
}
func (r *prepareCleanupRuntimeV1) Destroy() error {
	if r.destroy.Add(1) == 1 {
		close(r.entered)
	}
	select {
	case <-r.release:
		return nil
	default:
		return errors.New("native cleanup remains unresolved")
	}
}

func TestSandboxPrepareRetainsSameDestroyOwnerBeforeCompletionV1(t *testing.T) {
	runtime := &prepareCleanupRuntimeV1{release: make(chan struct{}), entered: make(chan struct{})}
	s := &Supervisor{}
	s.retainSandboxPreparationV1(runtime)
	done := make(chan error, 1)
	go func() { done <- s.closeSandboxPrepareRuntimeV1(runtime, true) }()
	select {
	case <-runtime.entered:
	case <-time.After(time.Second):
		t.Fatal("the actual retained owner was never destroyed")
	}
	select {
	case err := <-done:
		t.Fatal("an unresolved native Destroy became completion", err)
	default:
	}
	if s.sb != runtime || runtime.kill.Load() != 1 || runtime.wait.Load() != 1 {
		t.Fatal("the original runtime or diagnostic wait was replaced")
	}
	close(runtime.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("successful native Destroy did not settle the retained owner")
	}
	if runtime.destroy.Load() < 2 {
		t.Fatal("cleanup did not retry the SAME owner")
	}
}
