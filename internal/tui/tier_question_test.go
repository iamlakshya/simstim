package tui

import (
	"context"
	"errors"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

func tierQuestionModel(t *testing.T, fail bool) (*Model, *hostConn, *fakeBackend) {
	t.Helper()
	be := labBackend()
	m := New(context.Background(), be, Options{})
	t.Cleanup(m.Close)
	h := m.hostByName("atlas-dev")
	be.tiers[h.name()] = client.TierEntry{Name: h.name(), Tier: client.TierShared, NeedsAnswer: true}
	h.tier, h.tierUnset = client.TierShared, true
	be.resolveTier = func(ctx context.Context, host client.Host) (client.Tier, error) {
		choice, err := m.askHostTier(ctx, host)
		if err != nil {
			return client.TierShared, err
		}
		if fail {
			return client.TierShared, errors.New("tier save failed")
		}
		be.mu.Lock()
		e := be.tiers[host.Name]
		e.Tier, e.NeedsAnswer = choice, false
		be.tiers[host.Name] = e
		be.mu.Unlock()
		return choice, nil
	}
	return m, h, be
}

func beginTierQuestion(t *testing.T, m *Model, h *hostConn, next func() tea.Cmd) (*hostTierQuestion, <-chan tea.Msg) {
	t.Helper()
	done := make(chan tea.Msg, 1)
	cmd := m.withHostTier(h, next)
	go func() { done <- cmd() }()
	select {
	case request := <-m.tierRequests:
		m.onTierQuestion(request)
	case <-time.After(time.Second):
		t.Fatal("foreground use never requested the tier sheet")
	}
	q, ok := m.modal.(*hostTierQuestion)
	if !ok {
		t.Fatalf("tier question modal = %T", m.modal)
	}
	return q, done
}

func finishTierQuestion(t *testing.T, m *Model, done <-chan tea.Msg) {
	t.Helper()
	select {
	case msg := <-done:
		m.onHostTierResolved(msg.(hostTierResolvedMsg))
	case <-time.After(time.Second):
		t.Fatal("tier question did not finish")
	}
}

func TestForegroundTierSheetHasNoDefaultAndWaitsForPersistence(t *testing.T) {
	m, h, _ := tierQuestionModel(t, false)
	ran := false
	q, done := beginTierQuestion(t, m, h, func() tea.Cmd { ran = true; return nil })
	if q.pick != -1 || q.key(m, keyMsg("enter")) != nil {
		t.Fatal("Enter selected a default tier")
	}
	select {
	case <-done:
		t.Fatal("unanswered sheet started the operation")
	default:
	}
	if ran || !h.tierUnset || h.tier != client.TierShared {
		t.Fatal("unanswered sheet changed trust or began the operation")
	}
	q.key(m, keyMsg("1"))
	if ran || !h.tierUnset {
		t.Fatal("key press cleared pending before the save result")
	}
	finishTierQuestion(t, m, done)
	if !ran || h.tierUnset || h.tier != client.TierOwn {
		t.Fatalf("saved answer did not resume foreground work: ran=%v pending=%v tier=%s", ran, h.tierUnset, h.tier)
	}
}

func TestForegroundTierFailureAndCancellationKeepPendingShared(t *testing.T) {
	for _, failure := range []string{"save failure", "escape", "ctrl+c", "close"} {
		t.Run(failure, func(t *testing.T) {
			m, h, _ := tierQuestionModel(t, failure == "save failure")
			ran := false
			q, done := beginTierQuestion(t, m, h, func() tea.Cmd { ran = true; return nil })
			switch failure {
			case "save failure":
				q.key(m, keyMsg("1"))
			case "escape":
				q.key(m, keyMsg("esc"))
			case "ctrl+c":
				m.onKey(keyMsg("ctrl+c"))
			case "close":
				m.Close()
			}
			finishTierQuestion(t, m, done)
			if ran || !h.tierUnset || h.tier != client.TierShared {
				t.Fatalf("failed choice changed trust: ran=%v pending=%v tier=%s", ran, h.tierUnset, h.tier)
			}
		})
	}
}

func TestTierQuestionCloseKeepsOwnedWizardCleanup(t *testing.T) {
	m := New(context.Background(), newFakeBackend(), Options{})
	cleaned := make(chan struct{})
	w := newWizard()
	w.host = client.Host{Name: "box", SSH: "box"}
	w.step, w.staged = wzTest, true
	w.pinScope = &wizardPin{abort: func() { close(cleaned) }}
	m.modal = &hostTierQuestion{previous: w}
	m.closeAfterProgram()
	select {
	case <-cleaned:
	default:
		t.Fatal("tier sheet hid the wizard's owned stage from final cleanup")
	}
}

func TestWizardTierSheetHasNoDefaultAnswer(t *testing.T) {
	w := newWizard()
	w.step = wzTier
	m := New(context.Background(), newFakeBackend(), Options{})
	defer m.Close()
	w.key(m, keyMsg("enter"))
	if w.tier.Valid() || w.step != wzTier {
		t.Fatalf("Enter chose a wizard tier: tier=%s step=%v", w.tier, w.step)
	}
}
