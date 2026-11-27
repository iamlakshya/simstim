package supervisor

import (
	"context"
	"errors"
	"testing"

	"github.com/iamlakshya/simstim/internal/host"
)

func TestDiskStopCancelsOnlyCurrentBootAndRetainsTypedFailure(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(context.Canceled)
	s := &Supervisor{generation: "current-run", bootContextGeneration: "current-run", bootCancel: cancel, quit: make(chan string, 1), rec: record{phase: host.LifeBooting}}
	why := diskFailure("disk_full", "allocated disk usage exceeded its limit")
	s.requestDiskStop("foreign-run", why)
	if ctx.Err() != nil || s.diskFailure != nil || s.captureFailed {
		t.Fatal("foreign watcher stopped current boot")
	}
	s.requestDiskStop("current-run", why)
	if !errors.Is(context.Cause(ctx), why) || s.diskStopReason() != why || !s.captureFailed || !s.stopIntent {
		t.Fatal("current disk stop lost cause or capture barrier")
	}
	later := diskFailure("disk_measure", "later measurement failure")
	s.requestDiskStop("current-run", later)
	if s.diskStopReason() != why {
		t.Fatal("a later watcher replaced original stop cause")
	}
}
func TestDiskStopQueuesLiveCurrentRunWithoutJoiningOrBlockingOnFullQueue(t *testing.T) {
	s := &Supervisor{generation: "current-run", quit: make(chan string, 1), rec: record{phase: host.LifeRunning}}
	s.quit <- "already stopping"
	s.requestDiskStop("current-run", diskFailure("disk_measure", "the actual filesystem could not be measured"))
	if !s.captureFailed || s.diskStopReason() == nil {
		t.Fatal("live measurement failure lost stop/capture state")
	}
	if reason := <-s.quit; reason != "already stopping" {
		t.Fatal("disk callback replaced another owned stop turn")
	}
	ended := &Supervisor{generation: "current-run", quit: make(chan string, 1), rec: record{phase: host.LifeEnded}}
	ended.requestDiskStop("current-run", diskFailure("disk_full", "too late"))
	if ended.diskStopReason() != nil || len(ended.quit) != 0 {
		t.Fatal("retired watcher changed an ended run")
	}
}
