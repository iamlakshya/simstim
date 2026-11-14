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
