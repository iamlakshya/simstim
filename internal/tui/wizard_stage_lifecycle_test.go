package tui

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/client"
)

type delayedStageBackend struct {
	*fakeBackend
	opened      chan struct{}
	release     chan struct{}
	once        sync.Once
	calls       atomic.Int32
	awaitCancel bool
}

func (b *delayedStageBackend) StagePinOwned(ctx context.Context, name string) (context.Context, func(), error) {
	b.calls.Add(1)
	bound, abort, err := client.StagePinOwned(ctx, name)
	close(b.opened)
	if b.awaitCancel {
		<-ctx.Done()
	} else {
		<-b.release
	}
	return bound, abort, err
}
func (b *delayedStageBackend) SaveHostPinContext(ctx context.Context, h client.Host) error {
	return client.SaveHostPinContext(ctx, h)
}
func (b *delayedStageBackend) unblock() { b.once.Do(func() { close(b.release) }) }

func TestLauncherJoinsAStageThatHasOpenedBeforeItsResultReturns(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	be := &delayedStageBackend{fakeBackend: newFakeBackend(), opened: make(chan struct{}), release: make(chan struct{})}
	defer be.unblock()
	m := New(context.Background(), be, Options{})
	w := newWizard()
	w.host = client.Host{Name: "late-stage", SSH: "late-stage.example"}
	m.modal = w
	command := w.probe(m)
	workerDone := make(chan struct{})
	go func() { command(); close(workerDone) }()
	<-be.opened
	paths := wizardStageFiles(t)
	if len(paths) != 1 {
		t.Fatalf("the stage did not open: %q", paths)
	}
	programDone := make(chan struct{})
	go func() { m.closeAfterProgram(); close(programDone) }()
	select {
	case <-programDone:
		t.Error("program returned before its late opened stage was closed")
	case <-time.After(pinCleanupGrace + 50*time.Millisecond):
	}
	be.unblock()
	select {
	case <-workerDone:
	case <-time.After(time.Second):
		t.Fatal("stage worker did not finish")
	}
	select {
	case <-programDone:
	case <-time.After(time.Second):
		t.Fatal("program did not join its stage cleanup")
	}
	for _, path := range paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("late opened stage remains: %v", err)
		}
	}
}

func TestLauncherDoesNotJoinADroppedStageCommand(t *testing.T) {
	m, w, be := delayedStageWizard(t)
	command := w.probe(m)
	m.modal = nil
	closed := make(chan struct{})
	go func() { m.closeAfterProgram(); close(closed) }()
	waitStageLifecycle(t, closed, "closing with an uninvoked command")
	if got := be.calls.Load(); got != 0 {
		t.Fatalf("a dropped command called StagePinOwned %d times", got)
	}
	// Tea can hold the old command after the program has closed. Invoking
	// that value must not create a new worker or a private stage.
	finished := make(chan struct{})
	go func() { command(); close(finished) }()
	waitStageLifecycle(t, finished, "the old command after program exit")
	if got := be.calls.Load(); got != 0 {
		t.Fatalf("an old command opened a stage after exit: %d calls", got)
	}
	if paths := wizardStageFiles(t); len(paths) != 0 {
		t.Fatalf("an old command left stages: %q", paths)
	}
}

func TestLauncherStopsACommandPausedBeforeStaging(t *testing.T) {
	m, w, be := delayedStageWizard(t)
	before := beforeStagePin
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	beforeStagePin = func() { close(entered); <-release }
	t.Cleanup(func() { beforeStagePin = before })
	command := w.probe(m)
	finished := make(chan struct{})
	go func() { command(); close(finished) }()
	waitStageLifecycle(t, entered, "the command before staging")
	closed := make(chan struct{})
	go func() { m.closeAfterProgram(); close(closed) }()
	waitStageLifecycle(t, closed, "closing before the stage worker begins")
	unblock()
	waitStageLifecycle(t, finished, "the stopped command")
	if got := be.calls.Load(); got != 0 {
		t.Fatalf("a stopped command called StagePinOwned %d times", got)
	}
}

func TestLauncherCloseStaysFastWhileAnOpenedStageIsUnpublished(t *testing.T) {
	m, w, be := delayedStageWizard(t)
	command := w.probe(m)
	finished := make(chan struct{})
	go func() { command(); close(finished) }()
	waitStageLifecycle(t, be.opened, "the opened stage")
	paths := wizardStageFiles(t)
	if len(paths) != 1 {
		t.Fatalf("the stage did not open: %q", paths)
	}
	closed := make(chan struct{})
	go func() { m.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Close exceeded its response budget while a stage result was blocked")
	}
	// Close starts cleanup. The final program boundary owns the remaining
	// wait after the terminal has been restored.
	programDone := make(chan struct{})
	go func() { m.closeAfterProgram(); close(programDone) }()
	select {
	case <-programDone:
		t.Error("program exited while its stage worker still owned a file")
	case <-time.After(pinCleanupGrace + 50*time.Millisecond):
	}
	be.unblock()
	waitStageLifecycle(t, finished, "the released stage worker")
	waitStageLifecycle(t, programDone, "the final stage join")
	for _, path := range paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("stage remains after program exit: %v", err)
		}
	}
}

func TestLauncherLateStageCleanupKeepsAReplacementStage(t *testing.T) {
	m, w, be := delayedStageWizard(t)
	command := w.probe(m)
	finished := make(chan struct{})
	go func() { command(); close(finished) }()
	waitStageLifecycle(t, be.opened, "the first stage")
	first := wizardStageFiles(t)
	_, abort, err := client.StagePinOwned(context.Background(), w.host.Name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(abort)
	replacement := wizardStageFiles(t)
	if len(first) != 1 || len(replacement) != 1 || first[0] == replacement[0] {
		t.Fatalf("replacement stage = %q, first %q", replacement, first)
	}
	programDone := make(chan struct{})
	go func() { m.closeAfterProgram(); close(programDone) }()
	select {
	case <-programDone:
		t.Error("program exited before joining the old stage worker")
	case <-time.After(pinCleanupGrace + 50*time.Millisecond):
	}
	be.unblock()
	waitStageLifecycle(t, finished, "the old stage result")
	waitStageLifecycle(t, programDone, "the old stage cleanup")
	if current := wizardStageFiles(t); len(current) != 1 || current[0] != replacement[0] {
		t.Fatalf("old stage cleanup changed its replacement: %q", current)
	}
}

func delayedStageWizard(t *testing.T) (*Model, *wizard, *delayedStageBackend) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	be := &delayedStageBackend{fakeBackend: newFakeBackend(), opened: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(be.unblock)
	m := New(context.Background(), be, Options{})
	w := newWizard()
	w.host = client.Host{Name: "late-stage", SSH: "late-stage.example"}
	m.modal = w
	return m, w, be
}

func waitStageLifecycle(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for " + what)
	}
}

func TestLauncherOwnsAStageWhoseProbeResultWasDropped(t *testing.T) {
	m, w, be := stageRetryWizard(t)
	_ = w.probe(m)()
	if files := wizardStageFiles(t); len(files) != 1 {
		t.Fatalf("probe did not leave its owned stage: %q", files)
	}
	// The command result and modal are gone. Resource ownership belongs to
	// the launcher even when there is no wizard left to process the result.
	m.modal = nil
	m.closeAfterProgram()
	assertWizardStage(t, be, false)
}

func TestLauncherCancelsAnOpenedStageWhoseWizardIsGone(t *testing.T) {
	m, w, be := delayedStageWizard(t)
	be.awaitCancel = true
	command := w.probe(m)
	t.Cleanup(w.cancelOperation)
	finished := make(chan struct{})
	go func() { command(); close(finished) }()
	waitStageLifecycle(t, be.opened, "the stage waiting for cancellation")
	m.modal = nil
	programDone := make(chan struct{})
	go func() { m.closeAfterProgram(); close(programDone) }()
	waitStageLifecycle(t, finished, "cancelling the retired stage worker")
	waitStageLifecycle(t, programDone, "joining the retired stage worker")
	if files := wizardStageFiles(t); len(files) != 0 {
		t.Fatalf("the retired stage worker left files: %q", files)
	}
}
