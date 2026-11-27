package tui

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

func TestForegroundTierResolverAndWatcherJoinAtHandoff(t *testing.T) {
	for _, tc := range []struct{ operation, gate string }{{"browse", "resolver"}, {"browse", "watcher"}, {"attach", "resolver"}, {"attach", "watcher"}} {
		gated := tc.gate
		t.Run(tc.operation+"/"+gated, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				m, h, be := tierQuestionModel(t, false)
				release := make(chan struct{})
				var once sync.Once
				letGo := func() { once.Do(func() { close(release) }) }
				defer func() { m.cancelTier(); letGo(); m.Close() }()
				stopped := make(chan struct{}, 1)
				m.tierWait = func(wait func() tea.Msg) tea.Msg {
					msg := wait()
					_, resolver := msg.(tierAnswer)
					if resolver == (gated == "resolver") {
						stopped <- struct{}{}
						<-release
					}
					return msg
				}
				ran := false
				result := make(chan tea.Msg, 1)
				cmd := m.withHostTier(h, func() tea.Cmd { ran = true; return nil })
				if tc.operation == "attach" {
					h.sessions = []client.Session{{ID: "abcd2345", Name: "job", State: "idle", Alive: true}}
					h.loaded = true
					m.screen, m.cur, m.sel = scrSessions, h.name(), sessKey{h.name(), "abcd2345"}
					m.autoSel, m.handoff = false, true
					m.syncSel()
					cmd = m.attachSelected()
					if cmd == nil {
						t.Fatal("explicit attach skipped foreground tier resolution")
					}
				}
				go func() { result <- cmd() }()
				synctest.Wait()
				request := tierBuffered(t, m.tierRequests, "foreground tier request")
				watched := make(chan tea.Msg, 1)
				watch := m.onTierQuestion(request)
				go func() { watched <- watch() }()
				synctest.Wait()
				paused := make(chan struct{}, 1)
				go func() { m.pauseBg(); paused <- struct{}{} }()
				synctest.Wait()
				tierBuffered(t, stopped, "cancelled "+gated)
				select {
				case <-paused:
					t.Fatalf("handoff did not join the %s", gated)
				default:
				}
				if request.ctx.Err() != context.Canceled || m.ctx.Err() != nil {
					t.Fatal("foreground tier work has the wrong cancellation owner")
				}
				if ran || !h.tierUnset || h.tier != client.TierShared {
					t.Fatal("cancelled question began the operation or changed the pending shared tier")
				}
				letGo()
				synctest.Wait()
				tierBuffered(t, paused, "joined foreground tier handoff")
				msg, ok := tierBuffered(t, result, "cancelled resolver result").(hostTierResolvedMsg)
				if !ok || !errors.Is(msg.err, context.Canceled) {
					t.Fatalf("resolver returned %+v", msg)
				}
				m.onHostTierResolved(msg)
				tierBuffered(t, watched, "retired sheet watcher")
				be.mu.Lock()
				entry := be.tiers[h.name()]
				be.mu.Unlock()
				if ran || m.pendingAttach != nil || m.jacking != nil || !h.tierUnset || h.tier != client.TierShared || entry.Tier != client.TierShared || !entry.NeedsAnswer {
					t.Fatal("cancelled tier work published trust or ran its continuation")
				}
			})
		})
	}
}

func TestCancelledTierSheetRestoresPreviousOwnerBeforeResume(t *testing.T) {
	for _, previous := range []string{"wizard", "guide"} {
		t.Run(previous, func(t *testing.T) {
			m, cancel := tierHandoffModel()
			defer func() { cancel(); m.closeAfterProgram() }()
			var cleanup atomic.Int32
			w := newWizard()
			w.step, w.run, w.checks = wzTest, true, []check{{state: ckRunning}}
			w.staged, w.pinScope = true, &wizardPin{abort: func() { cleanup.Add(1) }}
			g := &guide{step: gdRentConfig, run: true, checks: []check{{state: ckRunning}}}
			var prior modal = w
			if previous == "guide" {
				prior = g
			}
			old := m.backgroundContext()
			m.pauseBg()
			m.modal = &hostTierQuestion{request: tierQuestionRequest{ctx: old}, previous: prior}
			m.openBg(m.ctx)
			called := false
			_ = m.resumeAfterHandoff(func() tea.Cmd {
				called = true
				if m.modal != prior {
					t.Fatalf("resume callback still sees %T instead of previous %s", m.modal, previous)
				}
				if previous == "wizard" && (w.run || w.err == nil || w.checks[0].state != ckFailed) {
					t.Fatal("restored wizard did not receive interrupted-operation handling")
				}
				if previous == "guide" && (g.run || g.err == nil || g.checks[0].state != ckFailed) {
					t.Fatal("restored guide did not receive interrupted-operation handling")
				}
				return nil
			})
			if !called || cleanup.Load() != 0 {
				t.Fatal("resume skipped its callback or disposed an owned stage prematurely")
			}
			m.closeAfterProgram()
			want := int32(0)
			if previous == "wizard" {
				want = 1
			}
			if cleanup.Load() != want {
				t.Fatalf("owned stage cleanup ran %d times, want %d", cleanup.Load(), want)
			}
		})
	}
}
