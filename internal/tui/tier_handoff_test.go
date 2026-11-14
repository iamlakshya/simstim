package tui

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

// tierHandoffModel starts no provider, bridge or terminal command. Each test
// executes only the owned commands that its handoff boundary needs.
func tierHandoffModel() (*Model, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	m := New(ctx, newFakeBackend(), Options{})
	m.after = func(time.Duration, tea.Msg) tea.Cmd { return nil }
	m.sshHosts = []string{}
	return m, cancel
}

func tierCommand(cmd tea.Cmd, out chan<- tea.Msg) {
	if cmd == nil {
		return
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, next := range batch {
			go tierCommand(next, out)
		}
	} else if msg != nil {
		out <- msg
	}
}

func tierBuffered[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	default:
		t.Fatalf("%s did not reach its deterministic gate", what)
		var zero T
		return zero
	}
}

func TestTierIdleReaderCancelsAndJoinsBeforeHandoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, cancel := tierHandoffModel()
		release := make(chan struct{})
		var once sync.Once
		letGo := func() { once.Do(func() { close(release) }) }
		defer func() { cancel(); letGo(); m.Close() }()
		entered, cancelled := make(chan struct{}, 1), make(chan struct{}, 1)
		m.tierWait = func(wait func() tea.Msg) tea.Msg {
			entered <- struct{}{}
			msg := wait()
			cancelled <- struct{}{}
			<-release
			return msg
		}
		ended := make(chan tea.Msg, 1)
		cmd := m.waitForTierQuestion()
		go func() { ended <- cmd() }()
		synctest.Wait()
		tierBuffered(t, entered, "idle tier reader")
		paused := make(chan struct{}, 1)
		go func() { m.pauseBg(); paused <- struct{}{} }()
		synctest.Wait()
		tierBuffered(t, cancelled, "cancelled idle tier reader")
		select {
		case <-paused:
			t.Fatal("handoff returned before the owned reader joined")
		default:
		}
		if m.ctx.Err() != nil {
			t.Fatal("handoff cancelled the launcher instead of its current reader")
		}
		letGo()
		synctest.Wait()
		tierBuffered(t, paused, "joined handoff")
		if msg := tierBuffered(t, ended, "retired reader"); msg != nil {
			t.Fatalf("retired reader returned %T", msg)
		}
	})
}

func TestDeferredTierReaderCannotConsumeTheNextProgramsQuestion(t *testing.T) {
	m, cancel := tierHandoffModel()
	defer func() { cancel(); m.Close() }()
	old := m.waitForTierQuestion()
	m.pauseBg()
	m.openBg(m.ctx)
	request := tierQuestionRequest{ctx: m.backgroundContext(), host: client.Host{Name: "box"}, answer: make(chan tierAnswer, 1), ended: make(chan struct{})}
	m.tierRequests <- request
	if msg := old(); msg != nil {
		t.Fatalf("retired deferred reader consumed the next question: %T", msg)
	}
	current := m.waitForTierQuestion()()
	got, ok := current.(tierQuestionRequest)
	if !ok || got.answer != request.answer {
		t.Fatalf("current reader got %T instead of its question", current)
	}
}

func TestRepeatedAttachResumeHasOneCurrentTierReader(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, cancel := tierHandoffModel()
		defer func() { cancel(); m.Close() }()
		var active atomic.Int32
		m.tierWait = func(wait func() tea.Msg) tea.Msg {
			active.Add(1)
			defer active.Add(-1)
			return wait()
		}
		out := make(chan tea.Msg, 32)
		for cycle := range 3 {
			m.pauseBg()
			if active.Load() != 0 {
				t.Fatal("the previous program retained a tier reader")
			}
			m.pendingAttach = &pendingAttach{host: "box", id: "abcd2345", opts: client.AttachOptions{Host: client.Host{Name: "box"}, Session: "abcd2345", State: m.attach}}
			program, err := m.runPendingAttach(m.ctx, nil, nil, false)
			if err != nil || program == nil {
				t.Fatalf("resume %d: %v", cycle, err)
			}
			go tierCommand(program.Init(), out)
			synctest.Wait()
			if n := active.Load(); n != 1 {
				t.Fatalf("resume %d has %d current tier readers, want one", cycle, n)
			}
			answered := make(chan tierAnswer, 1)
			go func() {
				tier, err := m.askHostTier(m.backgroundContext(), client.Host{Name: "box"})
				answered <- tierAnswer{tier, err}
			}()
			synctest.Wait()
			var request tierQuestionRequest
			for len(out) > 0 {
				if r, ok := (<-out).(tierQuestionRequest); ok {
					request = r
				}
			}
			if request.answer == nil {
				t.Fatal("resumed program did not receive its foreground question")
			}
			go tierCommand(m.onTierQuestion(request), out)
			synctest.Wait()
			q := m.modal.(*hostTierQuestion)
			q.pick = 0
			go tierCommand(q.finish(m), out)
			synctest.Wait()
			answer := tierBuffered(t, answered, "foreground tier answer after attach")
			if answer.err != nil || answer.tier != client.TierOwn {
				t.Fatalf("resume %d answer = %+v", cycle, answer)
			}
			for len(out) > 0 {
				if ended, ok := (<-out).(tierQuestionEndedMsg); ok {
					if next := m.onTierQuestionEnded(ended); next != nil {
						t.Fatal("completed sheet armed another reader")
					}
				}
			}
			if m.modal != nil || active.Load() != 1 {
				t.Fatalf("resume %d kept a sheet or duplicate reader: modal=%T readers=%d", cycle, m.modal, active.Load())
			}
		}
	})
}
