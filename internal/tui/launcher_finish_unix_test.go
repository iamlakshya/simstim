//go:build !windows

package tui

import (
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/muesli/cancelreader"
	"golang.org/x/term"

	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/keyread"
)

type gatedLauncherCancel struct {
	*demoHeldReader
	cancelling     chan struct{}
	continueCancel chan struct{}
}

func (r gatedLauncherCancel) Cancel() bool {
	close(r.cancelling)
	<-r.continueCancel
	return false
}

// A separate quit can return Program.Run while the handoff command is still
// stopping input. Both callers must observe the same completed stop before
// deciding whether another command may read the terminal.
func TestConcurrentLauncherFinishJoinsTheSameStop(t *testing.T) {
	terminal := hosttest.NewTerminal(t, 90, 30)
	held := newDemoHeldReader(false)
	reader := gatedLauncherCancel{demoHeldReader: held, cancelling: make(chan struct{}), continueCancel: make(chan struct{})}
	restoreReader := SetLauncherReader(func(io.Reader) (cancelreader.CancelReader, error) { return reader, nil })
	oldWait := waitLauncherFinish
	joined := make(chan struct{})
	waitLauncherFinish = func(done <-chan struct{}) { close(joined); <-done }
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(reader.continueCancel) }) }
	t.Cleanup(func() {
		release()
		held.unblock()
		_ = keyread.Release(terminal.TTY)
		restoreReader()
		waitLauncherFinish = oldWait
	})
	li, err := startLauncherInput(terminal.TTY)
	if err != nil {
		t.Fatal(err)
	}
	readEnded := make(chan struct{})
	go func() { defer close(readEnded); _, _ = li.Read(make([]byte, 1)) }()
	<-held.entered
	first := make(chan bool, 1)
	go func() { first <- li.finish() }()
	<-reader.cancelling
	second := make(chan bool, 1)
	go func() { second <- li.finish() }()
	select {
	case <-joined:
		release()
	case result := <-second:
		release()
		<-first
		t.Fatalf("second finish returned before the active stop completed: stuck=%v", result)
	case <-time.After(5 * time.Second):
		t.Fatal("second finish did not join or return")
	}
	if !<-first || !<-second {
		t.Fatal("concurrent finish callers did not retain the stuck-reader result")
	}
	if err := keyread.CheckStuck(terminal.TTY); err == nil {
		t.Fatal("the active reader was not recorded")
	}
	held.mu.Lock()
	closes, early := held.closes, held.closedDuringRead
	held.mu.Unlock()
	if closes != 0 || early {
		t.Fatalf("closed active reader: closes=%d early=%v", closes, early)
	}
	held.unblock()
	<-readEnded
}

// A handoff command can be scheduled after its program exits. Repeating the
// finished stop must not restore modes over a later terminal owner's raw mode.
func TestLateLauncherFinishLeavesTheNextOwnerModeAlone(t *testing.T) {
	terminal := hosttest.NewTerminal(t, 90, 30)
	restoreReader := SetLauncherReader(func(io.Reader) (cancelreader.CancelReader, error) { return newNoopCancelReader(), nil })
	defer restoreReader()
	li, err := startLauncherInput(terminal.TTY)
	if err != nil {
		t.Fatal(err)
	}
	if li.finish() {
		t.Fatal("idle reader is stuck")
	}
	li.restoreTerminal()
	before, err := term.MakeRaw(int(terminal.TTY.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = term.Restore(int(terminal.TTY.Fd()), before) }()
	raw, err := term.GetState(int(terminal.TTY.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if li.finish() {
		t.Fatal("finished idle reader changed its result")
	}
	after, err := term.GetState(int(terminal.TTY.Fd()))
	if err != nil || !reflect.DeepEqual(raw, after) {
		t.Fatalf("late stop changed the next owner's mode: %v", err)
	}
}
