package tui

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

func TestLauncherProgramBoundaryJoinsRetiredCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := New(context.Background(), newFakeBackend(), Options{})
		entered := make(chan struct{})
		release := make(chan struct{})
		m.conns.onClose(func() { close(entered); <-release })
		finished := make(chan struct{})
		go func() { m.closeAfterProgram(); close(finished) }()
		<-entered
		// Time is virtual in this bubble. Let Close's bounded grace expire,
		// then wait for the finalizer to block on the outstanding cleanup.
		time.Sleep(pinCleanupGrace + time.Millisecond)
		synctest.Wait()
		select {
		case <-finished:
			t.Error("the program returned while its retired cleanup was blocked")
		default:
		}
		close(release)
		synctest.Wait()
		select {
		case <-finished:
		default:
			t.Error("the finalizer did not return after cleanup finished")
		}
	})
}

func TestLauncherCancelledFinalizerStillJoinsCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		m := New(ctx, newFakeBackend(), Options{})
		entered, release := make(chan struct{}), make(chan struct{})
		m.conns.onClose(func() { close(entered); <-release })
		cancel()
		finished := make(chan struct{})
		go func() { m.closeAfterProgramContext(ctx); close(finished) }()
		<-entered
		time.Sleep(pinCleanupGrace + time.Millisecond)
		synctest.Wait()
		select {
		case <-finished:
			t.Error("cancellation let the finalizer skip owned cleanup")
		default:
		}
		close(release)
		synctest.Wait()
		select {
		case <-finished:
		default:
			t.Error("the cancelled finalizer did not join finished cleanup")
		}
	})
}
