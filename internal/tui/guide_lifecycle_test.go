package tui

import (
	"context"
	"io"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/onboard"
)

type lifecycleLocal struct {
	onboard.Local
	create func(context.Context, string, string, io.Writer) (client.Host, error)
}

func (l lifecycleLocal) Create(ctx context.Context, kind, name string, out io.Writer) (client.Host, error) {
	return l.create(ctx, kind, name, out)
}

func lifecycleWait[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatal("the guide operation did not reach its gate")
		var zero T
		return zero
	}
}

func lifecycleCreate(t *testing.T, m *Model) (*guide, tea.Cmd) {
	t.Helper()
	g := ownerGuide(t, m)
	ownerChoose(t, m, g, onboard.Docker)
	g.in.set("made")
	return g, g.create(m)
}

// The recovered review witness paused synchronously, then released cleanup
// after pause returned. Run pause concurrently and check it while cleanup is
// still held, after the other goroutines have settled.
func TestGuideHandoffWaitsForCreateCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, h, _, be, _ := newHandoffCommandModel(t)
		entered, cleanup, exited := make(chan context.Context, 1), make(chan struct{}), make(chan struct{})
		var release sync.Once
		letGo := func() { release.Do(func() { close(cleanup) }) }
		t.Cleanup(letGo)
		m.be = ownerCreateBackend{Backend: be, local: lifecycleLocal{Local: be.Local(), create: func(ctx context.Context, _, _ string, _ io.Writer) (client.Host, error) {
			entered <- ctx
			<-ctx.Done()
			<-cleanup
			close(exited)
			return client.Host{}, ctx.Err()
		}}}
		g, cmd := lifecycleCreate(t, m)
		reply := make(chan tea.Msg, 1)
		go func() { reply <- cmd() }()
		ctx := lifecycleWait(t, entered)
		if ctx.Value(handoffContextKey{}) != "kept" {
			t.Fatal("the provider lost the launcher's context values")
		}
		p := &promptState{mode: promptNew, host: h.name(), starting: true}
		m.prompt, m.handoff = p, true
		_ = m.update(startedMsg{
			startReq: startReq{host: h.name(), attach: true, line: p},
			s:        &client.Session{ID: "s2", Name: "newjob", Agent: "shell", State: "idle", Alive: true},
		})
		if m.pendingAttach == nil {
			t.Fatal("the quick-start result did not request its deferred attach")
		}
		paused := make(chan bool, 1)
		go func() {
			m.pauseBg()
			select {
			case <-exited:
				paused <- true
			default:
				paused <- false
			}
		}()
		lifecycleWait(t, ctx.Done())
		// Keep cleanup held until every runnable goroutine has settled. A pause
		// that did not join the provider must have returned by this point.
		synctest.Wait()
		select {
		case <-paused:
			t.Fatal("the launcher handed off before the provider cleaned up")
		default:
		}
		letGo()
		if !lifecycleWait(t, paused) {
			t.Fatal("the launcher handed off before the provider cleaned up")
		}
		_ = lifecycleWait(t, reply) // the old program drops this reply
		m.openBg(m.ctx)
		_ = m.resumeAfterAttach(attachDoneMsg{host: h.name(), id: "s2", name: "newjob", res: client.AttachResult{Detached: true}})
		if m.modal != g || g.run || g.err == nil || g.checks[0].state != ckFailed {
			t.Fatalf("the interrupted guide cannot retry: modal=%T run=%v err=%v checks=%+v", m.modal, g.run, g.err, g.checks)
		}
		m.be = be
		_ = g.key(m, tea.KeyPressMsg{Code: tea.KeyEnter})
		_ = m.update(ownerResult(t, g.create(m)))
		if w, ok := m.modal.(*wizard); !ok || w.host.Name != "made" {
			t.Fatal("retry did not prepare the newly created host")
		}
	})
}

func TestGuideUninvokedCreateRetiresWithoutStartingProvider(t *testing.T) {
	for _, leave := range []string{"close", "handoff", "escape", "folder-question", "setup-question", "setup-skip"} {
		t.Run(leave, func(t *testing.T) {
			m, h, _, be, _ := newHandoffCommandModel(t)
			var calls atomic.Int32
			m.be = ownerCreateBackend{Backend: be, local: lifecycleLocal{Local: be.Local(), create: func(context.Context, string, string, io.Writer) (client.Host, error) {
				calls.Add(1)
				return client.Host{}, context.Canceled
			}}}
			g, cmd := lifecycleCreate(t, m)
			done := g.done
			// On a broken stop-before-invocation path, unblock the deferred
			// Close cleanup after reporting the failed bounded assertion.
			t.Cleanup(func() {
				select {
				case <-done:
				default:
					close(done)
				}
			})
			var question startedMsg
			if leave == "folder-question" || leave == "setup-question" || leave == "setup-skip" {
				question = lifecycleQuestion(t, m, h, leave)
			}
			left := make(chan struct{})
			go func() {
				switch leave {
				case "close":
					m.Close()
				case "handoff":
					m.pauseBg()
					m.openBg(m.ctx)
					_ = m.resumeAfterExec(nil)
				case "escape":
					_ = g.back(m)
				default:
					_ = m.onStarted(question)
				}
				close(left)
			}()
			lifecycleWait(t, left)
			executeHandoffCommand(cmd)
			if calls.Load() != 0 || g.run {
				t.Fatalf("retired create started later: provider calls=%d run=%v", calls.Load(), g.run)
			}
		})
	}
}

func TestGuideDroppedSuccessfulCreateIsRetainedOnce(t *testing.T) {
	for _, leave := range []string{"close", "handoff", "folder-question", "setup-question", "setup-skip"} {
		t.Run(leave, func(t *testing.T) {
			m, h, _, be, _ := newHandoffCommandModel(t)
			g, cmd := lifecycleCreate(t, m)
			msg := ownerResult(t, cmd).(createdMsg)
			if msg.err != nil {
				t.Fatal(msg.err)
			}
			calls := be.callList()
			switch leave {
			case "close":
				m.Close()
			case "handoff":
				m.pauseBg()
				m.openBg(m.ctx)
				_ = m.resumeAfterExec(nil)
				w, ok := m.modal.(*wizard)
				if !ok || w.host.Name != "made" || !w.run || w.ctx == nil || w.ctx.Err() != nil {
					t.Fatal("resume lost the result or cancelled its newly started wizard")
				}
			default:
				_ = m.onStarted(lifecycleQuestion(t, m, h, leave))
			}
			if leave != "handoff" {
				if made := m.hostByName("made"); made == nil || made.conn != nil || made.status != stOffline {
					t.Fatal("retirement hid a completed saved host or started preparation")
				}
			}
			before := m.modal
			_ = m.update(msg)
			if m.modal != before || g.run || len(be.hosts) != 2 || be.hosts[1].Name != "made" || !reflect.DeepEqual(calls, be.callList()) {
				t.Fatal("a dropped result was applied twice or changed the saved machine")
			}
		})
	}
}

func TestGuideDroppedSSHReplyResetsRunningStepAtHandoff(t *testing.T) {
	for _, step := range []gdStep{gdRentUser, gdRentConfig} {
		t.Run(map[gdStep]string{gdRentUser: "user", gdRentConfig: "config"}[step], func(t *testing.T) {
			m, _, _, _, _ := newHandoffCommandModel(t)
			g := ownerGuide(t, m)
			g.step, g.plan.Name = step, "server"
			msg := ownerResult(t, g.sshCheck(m, "server"))
			m.pauseBg()
			m.openBg(m.ctx)
			_ = m.resumeAfterExec(nil)
			if g.run || g.err == nil || g.checks[0].state != ckFailed {
				t.Fatal("the interrupted SSH step cannot retry")
			}
			_ = m.update(msg)
			if m.modal != g || g.step != step {
				t.Fatal("an old SSH success skipped the interrupted step")
			}
			_ = m.update(ownerResult(t, g.sshCheck(m, "server")))
			if step == gdRentUser && g.step != gdRentName {
				t.Fatal("the SSH user step did not retry")
			}
			if step == gdRentConfig {
				if w, ok := m.modal.(*wizard); !ok || w.host.Name != "server" {
					t.Fatal("the SSH config step did not retry")
				}
			}
		})
	}
}
