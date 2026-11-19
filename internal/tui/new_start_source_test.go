package tui

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/client"
)

func pumpNewHostUntil(t *testing.T, hs *harness, entered <-chan context.Context) context.Context {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case ctx := <-entered:
			return ctx
		case message := <-hs.results:
			hs.deliver(message)
		case <-timer.C:
			t.Fatal("the selected host operation did not reach its owned stage")
			return nil
		}
	}
}

func TestNewStartReconnectsStaleSourceBeforeSelectingValues(t *testing.T) {
	hs := newPlacementFixture(t, "gcp-dev")
	hs.key("n")
	dialog := hs.m.modal.(*newSession)
	dialog.name.set("fresh-source")
	host := hs.m.hostByName("gcp-dev")
	old := unclean(host.conn).(*fakeConn)
	old.mu.Lock()
	old.savedSourceErr = &client.UserError{Code: "host_changed", Msg: "the original Source ended", Hint: "Reconnect before a new request.", Kind: client.KindRefused}
	old.mu.Unlock()
	hs.key("ctrl+enter")
	if !old.isClosed() || unclean(host.conn) == old || hs.be.dials["gcp-dev"] != 2 || len(hs.be.newArgs) != 1 {
		t.Fatalf("new start did not acquire a fresh pre-request connection: dials=%v args=%d", hs.be.dials, len(hs.be.newArgs))
	}
	reconnect, values, start := -1, -1, -1
	for index, call := range hs.be.callList() {
		switch {
		case call == "reconnect gcp-dev":
			reconnect = index
		case call == "secrets gcp-dev":
			values = index
		case strings.HasPrefix(call, "new gcp-dev agent="):
			start = index
		}
	}
	if reconnect < 0 || values <= reconnect || start <= values {
		t.Fatalf("reconnect happened inside or after value selection: %v", hs.be.callList())
	}
}

type selectedSourceFailureConn struct {
	Conn
	failure error
	checks  atomic.Int32
}

func (c *selectedSourceFailureConn) CheckSavedSource(context.Context) error {
	c.checks.Add(1)
	return c.failure
}
func (c *selectedSourceFailureConn) CheckNewGrants(context.Context, []string, bool) error {
	return c.failure
}

func TestSelectedNewRequestDoesNotReconnectOrRefreshItsFailure(t *testing.T) {
	hs := newPlacementFixture(t, "gcp-dev")
	hs.key("n")
	dialog := hs.m.modal.(*newSession)
	host := hs.m.hostByName(dialog.host)
	failure := &client.UserError{Code: "host_changed", Msg: "selected Source ended", Hint: "Start a new logical request.", Kind: client.KindRefused}
	connection := &selectedSourceFailureConn{Conn: unclean(host.conn), failure: failure}
	host.conn = cleanConn{connection}
	request := client.NewGrantRequest(false, false)
	t.Cleanup(func() { _ = request.Close() })
	dialog.starting = true
	hs.do(hs.m.startSessionReady(host, startReq{host: host.name(), args: client.NewArgs{Agent: "shell", Cwd: "~", Name: "held-work"}, grants: request, form: dialog}))
	if connection.checks.Load() != 0 || dialog.err != failure || len(hs.be.newArgs) != 0 || len(hs.be.secretsFrom) != 0 {
		t.Fatalf("an existing request was refreshed or sent values: checks=%d error=%v", connection.checks.Load(), dialog.err)
	}
	for _, call := range hs.be.callList() {
		if strings.HasPrefix(call, "reconnect ") {
			t.Fatal("an existing request reconnected", hs.be.callList())
		}
	}
}

func TestCancelledPreRequestReconnectCannotStartFromARetiredForm(t *testing.T) {
	hs := newPlacementFixture(t, "gcp-dev")
	hs.key("n")
	dialog := hs.m.modal.(*newSession)
	dialog.name.set("no-late-request")
	host := hs.m.hostByName(dialog.host)
	old := unclean(host.conn).(*fakeConn)
	old.mu.Lock()
	old.savedSourceErr = client.ErrClosed
	old.mu.Unlock()
	entered, release := make(chan context.Context, 1), make(chan struct{})
	var once sync.Once
	releaseDial := func() { once.Do(func() { close(release) }) }
	defer releaseDial()
	hs.be.retire = func(ctx context.Context, _ client.Host) error { entered <- ctx; <-release; return ctx.Err() }
	hs.start(dialog.submit(hs.m, false))
	_ = pumpNewHostUntil(t, hs, entered)
	hs.deliver(keyMsg("esc"))
	if hs.m.modal != nil {
		t.Fatal("Escape kept the original form open")
	}
	releaseDial()
	hs.do(nil)
	if len(hs.be.newArgs) != 0 || len(hs.be.secretsFrom) != 0 || len(hs.be.attached) != 0 || !host.online() {
		t.Fatal("a completed shared reconnect started work for the retired form")
	}
}

func TestCancelledDefaultTierCheckCannotOpenALateForm(t *testing.T) {
	hs := newPlacementFixture(t, "gcp-dev")
	entered, release := make(chan context.Context, 1), make(chan struct{})
	var once sync.Once
	releaseTier := func() { once.Do(func() { close(release) }) }
	defer releaseTier()
	hs.be.resolveTier = func(ctx context.Context, _ client.Host) (client.Tier, error) {
		entered <- ctx
		<-release
		return client.TierOwn, nil
	}
	hs.start(hs.m.openQuickStart())
	ctx := pumpNewHostUntil(t, hs, entered)
	hs.deliver(keyMsg("esc"))
	if ctx.Err() == nil {
		t.Fatal("Escape kept the default's tier check active")
	}
	releaseTier()
	hs.do(nil)
	if hs.m.modal != nil || hs.m.prompt != nil || len(hs.be.newArgs) != 0 {
		t.Fatal("an old tier result opened a new default form")
	}
}
