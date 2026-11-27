package tui

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

func lifecycleQuestion(t *testing.T, m *Model, h *hostConn, kind string) startedMsg {
	t.Helper()
	var question any = client.FolderQuestion{Folder: "/work/new"}
	code := api.CodeNoFolder
	if kind == "setup-question" {
		question, code = client.SetupQuestion{Repo: "/work/app", Script: "/work/app/.setup"}, api.CodeSetupTrust
	}
	if kind == "setup-skip" {
		question, code = nil, api.CodeSetupOutside
	}
	data, err := json.Marshal(question)
	if err != nil {
		t.Fatal(err)
	}
	p := &promptState{mode: promptNew, host: h.name(), starting: true}
	m.prompt = p
	return startedMsg{startReq: startReq{host: h.name(), line: p}, err: &client.UserError{Msg: "the start needs an answer", Hint: "Answer the question.", Code: code, Data: data}}
}

func lifecycleIsQuestion(t *testing.T, m *Model, kind string) {
	t.Helper()
	if kind == "setup-skip" {
		if _, ok := m.modal.(*setupSkipAsk); !ok {
			t.Fatalf("the setup skip question was lost: %T", m.modal)
		}
		return
	}
	if kind == "setup-question" {
		if _, ok := m.modal.(*setupAsk); !ok {
			t.Fatalf("the setup question was lost: %T", m.modal)
		}
		return
	}
	if _, ok := m.modal.(*makeFolderAsk); !ok {
		t.Fatalf("the folder question was lost: %T", m.modal)
	}
}

func TestStartQuestionJoinsAndDisposesTheActiveGuide(t *testing.T) {
	for _, kind := range []string{"folder-question", "setup-question", "setup-skip"} {
		t.Run(kind, func(t *testing.T) {
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
			question := lifecycleQuestion(t, m, h, kind)
			installed := make(chan bool, 1)
			go func() {
				_ = m.onStarted(question)
				select {
				case <-exited:
					installed <- true
				default:
					installed <- false
				}
			}()
			lifecycleWait(t, ctx.Done())
			letGo()
			if !lifecycleWait(t, installed) {
				t.Fatal("the start replaced its guide before provider cleanup ended")
			}
			stale := lifecycleWait(t, reply)
			if g.run || g.cancel != nil || g.done != nil {
				t.Fatal("the old guide kept a live operation after modal replacement")
			}
			lifecycleIsQuestion(t, m, kind)
			_ = m.update(stale)
			lifecycleIsQuestion(t, m, kind)
		})
	}
}

func TestStartQuestionRetiresPreparedWizardConnection(t *testing.T) {
	for _, kind := range []string{"folder-question", "setup-question", "setup-skip"} {
		for _, deliver := range []bool{false, true} {
			name := kind + "/dropped-setup"
			if deliver {
				name = kind + "/prepared-connection"
			}
			t.Run(name, func(t *testing.T) {
				m, h, w, _, _ := newHandoffCommandModel(t)
				w.host = client.Host{Name: "new", SSH: "new"}
				msg := ownerResult(t, w.setup(m)).(setupMsg)
				if msg.err != nil || msg.conn == nil {
					t.Fatalf("setup fixture failed: %+v", msg)
				}
				var save tea.Cmd
				if deliver {
					save = m.onSetup(msg)
				}
				ctx, seq := w.ctx, w.seq
				cleanup := m.onStarted(lifecycleQuestion(t, m, h, kind))
				if ctx.Err() != context.Canceled || w.seq == seq || w.run || w.checking || w.starting || w.conn != nil {
					t.Fatal("the old wizard kept operation or connection ownership")
				}
				executeHandoffCommand(cleanup)
				if !deliver {
					m.pauseBg() // the old program drops setupMsg entirely
				}
				if !unclean(msg.conn).(*handoffConn).isClosed() {
					t.Fatal("the replaced wizard's unadopted connection leaked")
				}
				executeHandoffCommand(save)
				executeHandoffCommand(m.onSetup(msg))
				lifecycleIsQuestion(t, m, kind)
			})
		}
	}
}

func TestGuideSuccessDuringCancellationStillPreparesSavedHost(t *testing.T) {
	m, _, _, be, _ := newHandoffCommandModel(t)
	entered := make(chan context.Context, 1)
	local := be.Local()
	m.be = ownerCreateBackend{Backend: be, local: lifecycleLocal{Local: local, create: func(ctx context.Context, kind, name string, out io.Writer) (client.Host, error) {
		entered <- ctx
		<-ctx.Done()
		// The provider committed its saved host before noticing cancellation.
		// Its successful result must survive losing the program's delivery.
		return local.Create(ctx, kind, name, out)
	}}}
	g, cmd := lifecycleCreate(t, m)
	reply := make(chan tea.Msg, 1)
	go func() { reply <- cmd() }()
	_ = lifecycleWait(t, entered)
	m.pauseBg()
	msg := lifecycleWait(t, reply).(createdMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	m.openBg(m.ctx)
	_ = m.resumeAfterExec(nil)
	w, ok := m.modal.(*wizard)
	if !ok || w.host.Name != "made" || !w.run || g.run || len(be.hosts) != 2 {
		t.Fatal("cancellation hid a completed successful provider result")
	}
	_ = m.update(msg)
	if m.modal != w || len(be.hosts) != 2 {
		t.Fatal("a late duplicate success replaced the current wizard")
	}
}
