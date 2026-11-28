package tui

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

func TestPickerCleansHostTextAndKeepsTheChosenValues(t *testing.T) {
	row := PickRow{Host: "box", Session: client.Session{
		ID: "a2", Name: "auth\u200b-refactor\x1b[2J", Agent: "shell\u202e", State: "working\u200b",
	}}
	p := newPicker("attach", []PickRow{row}, func() time.Time { return now })
	checkClean(t, "the picker", p.render())
	if row.Address() != "box/auth-refactor[2J" {
		t.Fatalf("the address is not the text shown: %q", row.Address())
	}
	p.filter = []rune("auth-refactor")
	if len(p.matches()) != 1 {
		t.Fatal("the picker cannot find the name that it shows")
	}
	_ = p.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	if p.chosen == nil || !reflect.DeepEqual(*p.chosen, row) {
		t.Fatalf("the picker changed the values sent back to the host: %+v", p.chosen)
	}
}

func TestPickerDropsInvalidSessionIDsFromCacheAndRefresh(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	good := client.Session{ID: "a2", Name: "raw\u200bname"}
	bad := client.Session{ID: "../invalid", Name: "unusable"}
	client.RememberSessions(map[string][]client.Session{"box": {bad, good}})
	rows := CachedSessions([]string{"box"})
	if len(rows) != 1 || !reflect.DeepEqual(rows[0].Session, good) {
		t.Fatalf("the cached picker rows changed valid values or kept a bad ID: %+v", rows)
	}
	p := newPicker("attach", []PickRow{{Host: "box", Session: bad}, {Host: "box", Session: good}}, func() time.Time { return now })
	if len(p.rows) != 1 || p.rows[0].Session.ID != good.ID {
		t.Fatal("the picker offered an invalid initial session ID")
	}
	_, cmd := p.Update(pickedRows{rows: []PickRow{{Host: "box", Session: bad}}})
	if !p.done || !errors.Is(p.failed, ErrNoSessions) || cmd == nil || len(p.rows) != 0 {
		t.Fatal("an invalid refreshed row kept the picker open or could be selected")
	}
}

func TestDeferredSetupAnswerDoesNotWriteAfterHandoff(t *testing.T) {
	m, h, _, be, _ := newHandoffCommandModel(t)
	p := &promptState{mode: promptNew, host: h.name(), starting: true}
	m.prompt = p
	ask := &setupAsk{startReq: startReq{host: h.name(), line: p}, q: client.SetupQuestion{
		Repo: "/work/app", Script: "/work/app/.setup", Hash: "checked-hash",
	}}
	m.modal = ask
	cmd := ask.key(m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	before := be.callList()
	m.pauseBg()
	executeHandoffCommand(cmd)
	if !reflect.DeepEqual(before, be.callList()) {
		t.Fatal("a deferred answer wrote setup trust or started a session after handoff")
	}
	if len(be.trust) != 0 {
		t.Fatal("the deferred answer kept trust after cancellation")
	}
}

type setupContextCall struct {
	ctx              context.Context
	host, repo, hash string
	run              bool
}

type setupContextBackend struct {
	Backend
	entered chan setupContextCall
	release <-chan struct{}
}

func (b setupContextBackend) RememberSetup(ctx context.Context, host, repo string, run bool, hash string) (*client.UserError, error) {
	b.entered <- setupContextCall{ctx: ctx, host: host, repo: repo, run: run, hash: hash}
	<-ctx.Done()
	<-b.release
	return b.Backend.RememberSetup(ctx, host, repo, run, hash)
}

func TestSetupAnswerPassesCurrentContextAndHashThroughBackend(t *testing.T) {
	m, h, _, be, _ := newHandoffCommandModel(t)
	m.pauseBg()
	m.openBg(m.ctx)
	current := m.backgroundContext()
	entered, release := make(chan setupContextCall, 1), make(chan struct{})
	var once sync.Once
	letGo := func() { once.Do(func() { close(release) }) }
	t.Cleanup(letGo)
	m.be = setupContextBackend{Backend: be, entered: entered, release: release}
	p := &promptState{mode: promptNew, host: h.name(), starting: true}
	m.prompt = p
	ask := &setupAsk{startReq: startReq{host: h.name(), line: p}, q: client.SetupQuestion{
		Repo: "/work/app", Script: "/work/app/.setup", Hash: "checked-hash",
	}}
	m.modal = ask
	cmd := ask.key(m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	finished := make(chan struct{})
	go func() { executeHandoffCommand(cmd); close(finished) }()
	call := lifecycleWait(t, entered)
	if call.ctx != current || call.ctx.Value(handoffContextKey{}) != "kept" || call.host != h.name() || call.repo != ask.q.Repo || call.hash != ask.q.Hash || !call.run {
		t.Fatalf("the backend lost the current operation or checked hash: %+v", call)
	}
	paused := make(chan struct{})
	go func() { m.pauseBg(); close(paused) }()
	lifecycleWait(t, call.ctx.Done())
	letGo()
	lifecycleWait(t, paused)
	lifecycleWait(t, finished)
	if call.ctx.Err() != context.Canceled || len(be.trust) != 0 {
		t.Fatal("the cancelled in-flight answer changed saved setup trust")
	}
}

func TestSetupAnswerBackendsRejectCancelledWrites(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name string
		be   Backend
	}{{"real", realBackend{}}, {"fake", newFakeBackend()}, {"demo", newDemoBackend(false)}} {
		t.Run(tc.name, func(t *testing.T) {
			warn, err := tc.be.RememberSetup(ctx, "box", "/work/app", true, "checked-hash")
			if !errors.Is(err, context.Canceled) || warn != nil {
				t.Fatalf("the backend accepted a cancelled answer: warning=%v error=%v", warn, err)
			}
		})
	}
	if got := client.LoadSetupAnswers("box"); len(got.Trusted) != 0 || len(got.Hashes) != 0 || len(got.Refused) != 0 {
		t.Fatal("the real backend persisted a cancelled answer")
	}
	be := newFakeBackend()
	if _, err := be.RememberSetup(context.Background(), "box", "/work/app", true, "kept-hash"); err != nil {
		t.Fatal(err)
	}
	warning := &client.UserError{Msg: "the answers file moved aside", Hint: "Check the saved answers.", Code: "setup_trust"}
	be.rememberWarn = warning
	if warn, err := be.RememberSetup(ctx, "box", "/work/app", false, ""); !errors.Is(err, context.Canceled) || warn != nil {
		t.Fatal("the fake accepted a cancelled refusal")
	}
	if got := be.trust["box"]; !got.Trusted["/work/app"] || got.Hashes["/work/app"] != "kept-hash" || len(got.Refused) != 0 || be.rememberWarn != warning {
		t.Fatal("the fake changed the previous hash, refusal or pending recovery warning")
	}
}
