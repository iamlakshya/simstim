//go:build !windows

package tui

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/client"
)

func holdWizardPinLock(t *testing.T) func() {
	t.Helper()
	path := filepath.Join(client.ConfigDir(), "ssh", client.HostFileKey("box")+".lock")
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
			_ = f.Close()
		})
	}
	t.Cleanup(release)
	return release
}

func TestWizardRetryCancellationWhileThePinLockIsHeld(t *testing.T) {
	for _, own := range []bool{true, false} {
		for _, stop := range []string{"esc", "close"} {
			name := "own/" + stop
			if !own {
				name = "another/" + stop
			}
			t.Run(name, func(t *testing.T) {
				m, w, be := stageRetryWizard(t)
				if own {
					openUncheckedWizardStage(t, m, w, be)
				} else if err := be.StagePin(context.Background(), w.host.Name); err != nil {
					t.Fatal(err)
				}
				release := holdWizardPinLock(t)
				defer release()
				entered := make(chan struct{})
				if own {
					before := beforeStagePin
					beforeStagePin = func() { close(entered) }
					t.Cleanup(func() { beforeStagePin = before })
				} else {
					be.entered = entered
				}
				cmd := w.probe(m)
				finished := make(chan struct{})
				go func() { _ = cmd(); close(finished) }()
				<-entered
				stopped := make(chan struct{})
				cleaned := make(chan struct{})
				go func() {
					if stop == "esc" {
						if cleanup := w.back(m); cleanup != nil {
							go func() { cleanup(); close(cleaned) }()
						} else {
							close(cleaned)
						}
					} else {
						m.Close()
						close(cleaned)
					}
					close(stopped)
				}()
				select {
				case <-stopped:
				case <-time.After(500 * time.Millisecond):
					release()
					<-stopped
					<-finished
					t.Fatal("the wizard waited for the pin lock before it stopped")
				}
				release()
				select {
				case <-finished:
				case <-time.After(2 * time.Second):
					t.Fatal("the cancelled stage did not finish after the lock was released")
				}
				select {
				case <-cleaned:
				case <-time.After(2 * time.Second):
					t.Fatal("the old stage cleanup did not finish after the lock was released")
				}
				assertWizardStage(t, be, !own)
			})
		}
	}
}

func TestWizardCloseStaysResponsiveWithADroppedCleanupAndHeldLock(t *testing.T) {
	m, w, be := stageRetryWizard(t)
	openUncheckedWizardStage(t, m, w, be)
	release := holdWizardPinLock(t)
	defer release()
	_ = w.back(m)
	_ = w.back(m)
	stopped := make(chan struct{})
	go func() { m.Close(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(500 * time.Millisecond):
		release()
		<-stopped
		t.Fatal("Close waited for an external pin lock")
	}
	release()
	finished := make(chan struct{})
	go func() { m.closeAfterProgram(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("the final program cleanup did not finish after the lock was released")
	}
	assertWizardStage(t, be, false)
}
