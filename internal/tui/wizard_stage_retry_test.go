package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/client"
)

// retryStageBackend keeps the real stage file as well as the fake's stage
// record, so these checks cover both resources without connecting to a host.
type retryStageBackend struct {
	*fakeBackend
	stageErr    error
	gate        <-chan struct{}
	entered     chan struct{}
	installGate <-chan struct{}
	installing  chan struct{}
	owners      map[string]*retryPinOwner
}

func (b *retryStageBackend) StagePin(ctx context.Context, name string) error {
	_, _, err := b.StagePinOwned(ctx, name)
	return err
}

// closeStages releases only stages that this fixture actually opened. A
// legacy call shape still captures the owned abort rather than abort by name.
func (b *retryStageBackend) closeStages() {
	b.mu.Lock()
	var aborts []func()
	for _, owner := range b.owners {
		if owner.abort != nil {
			aborts = append(aborts, owner.abort)
		}
	}
	b.mu.Unlock()
	for _, abort := range aborts {
		abort()
	}
}

func (b *retryStageBackend) AbortPin(name string) {
	client.AbortPin(name)
	b.mu.Lock()
	delete(b.owners, name)
	b.mu.Unlock()
	b.fakeBackend.AbortPin(name)
}

func stageRetryWizard(t *testing.T) (*Model, *wizard, *retryStageBackend) {
	t.Helper()
	return stageRetryWizardContext(t, context.Background())
}

func stageRetryWizardContext(t *testing.T, ctx context.Context) (*Model, *wizard, *retryStageBackend) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	be := &retryStageBackend{fakeBackend: newFakeBackend()}
	be.pinUnknown = true
	m := New(ctx, be, Options{})
	w := newWizard()
	w.host = client.Host{Name: "box", SSH: "box"}
	m.modal = w
	t.Cleanup(func() {
		m.Close()
		be.closeStages()
	})
	return m, w, be
}

func openUncheckedWizardStage(t *testing.T, m *Model, w *wizard, be *retryStageBackend) {
	t.Helper()
	msg := w.probe(m)().(probeMsg)
	w.onProbe(m, msg)
	if w.ask == nil || w.err != nil {
		t.Fatalf("first probe: ask %v, error %v", w.ask, w.err)
	}
	assertWizardStage(t, be, true)
}

func assertWizardStage(t *testing.T, be *retryStageBackend, want bool) {
	t.Helper()
	be.mu.Lock()
	_, inMemory := be.staged["box"]
	be.mu.Unlock()
	files, err := filepath.Glob(filepath.Join(client.ConfigDir(), "ssh", ".known_hosts-*-staged-*"))
	if err != nil {
		t.Fatal(err)
	}
	if inMemory != want || (len(files) != 0) != want {
		t.Fatalf("stage: memory %v, files %q, want present %v", inMemory, files, want)
	}
}

func TestWizardRetryStageErrorRemovesTheEarlierStage(t *testing.T) {
	for _, stop := range []string{"esc", "close"} {
		for _, stageErr := range []error{context.DeadlineExceeded, errors.New("stage lock failed")} {
			t.Run(stop+"/"+stageErr.Error(), func(t *testing.T) {
				m, w, be := stageRetryWizard(t)
				openUncheckedWizardStage(t, m, w, be)
				be.stageErr = stageErr
				msg := w.probe(m)().(probeMsg)
				w.onProbe(m, msg)
				if !errors.Is(w.err, stageErr) {
					t.Fatalf("retry error %v, want %v", w.err, stageErr)
				}
				if stop == "esc" {
					if cmd := w.back(m); cmd != nil {
						cmd()
					}
				} else {
					m.Close()
				}
				assertWizardStage(t, be, false)
			})
		}
	}
}

func TestWizardStopsAfterStageBeforeProbeResult(t *testing.T) {
	for _, stop := range []string{"esc", "close"} {
		t.Run(stop, func(t *testing.T) {
			m, w, be := stageRetryWizard(t)
			msg := w.probe(m)().(probeMsg)
			assertWizardStage(t, be, true)
			if stop == "esc" {
				if cmd := w.back(m); cmd != nil {
					cmd()
				}
			} else {
				m.Close()
			}
			// A late result cannot restore ownership after the wizard stopped.
			w.onProbe(m, msg)
			assertWizardStage(t, be, false)
		})
	}
}

func TestWizardCancelsARetryThatWaitsToStage(t *testing.T) {
	for _, stop := range []string{"esc", "close"} {
		t.Run(stop, func(t *testing.T) {
			m, w, be := stageRetryWizard(t)
			openUncheckedWizardStage(t, m, w, be)
			be.gate, be.entered = make(chan struct{}), make(chan struct{})
			cmd := w.probe(m)
			finished := make(chan struct{})
			go func() { _ = cmd(); close(finished) }()
			<-be.entered
			if stop == "esc" {
				if cmd := w.back(m); cmd != nil {
					cmd()
				}
			} else {
				m.Close()
			}
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("retry did not stop after cancellation")
			}
			assertWizardStage(t, be, false)
		})
	}
}

func TestWizardFailedStageKeepsAnotherOperationsStage(t *testing.T) {
	for _, stop := range []string{"esc", "close"} {
		t.Run(stop, func(t *testing.T) {
			m, w, be := stageRetryWizard(t)
			if err := be.StagePin(context.Background(), w.host.Name); err != nil {
				t.Fatal(err)
			}
			be.stageErr = os.ErrPermission
			msg := w.probe(m)().(probeMsg)
			w.onProbe(m, msg)
			if stop == "esc" {
				if cmd := w.back(m); cmd != nil {
					cmd()
				}
			} else {
				m.Close()
			}
			assertWizardStage(t, be, true)
		})
	}
}

func TestWizardCancelledStageKeepsAnotherOperationsStage(t *testing.T) {
	for _, stop := range []string{"esc", "close"} {
		t.Run(stop, func(t *testing.T) {
			m, w, be := stageRetryWizard(t)
			if err := be.StagePin(context.Background(), w.host.Name); err != nil {
				t.Fatal(err)
			}
			be.gate, be.entered = make(chan struct{}), make(chan struct{})
			cmd := w.probe(m)
			finished := make(chan struct{})
			go func() { _ = cmd(); close(finished) }()
			<-be.entered
			if stop == "esc" {
				if cmd := w.back(m); cmd != nil {
					cmd()
				}
			} else {
				m.Close()
			}
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("stage did not stop after cancellation")
			}
			assertWizardStage(t, be, true)
		})
	}
}
