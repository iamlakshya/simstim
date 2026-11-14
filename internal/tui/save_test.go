package tui

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

type saveContextKey struct{}

// A cancel before the CAS transaction reads the saved hosts saves nothing:
// the transaction takes the launcher's owned context and values.
func TestACancelWhileTheHostListLoadsSavesNothing(t *testing.T) {
	be := newFakeBackend()
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), saveContextKey{}, "kept"))
	defer cancel()
	m := New(ctx, be, Options{Now: func() time.Time { return now }})
	t.Cleanup(func() { m.closeAfterProgramContext(ctx) })
	be.mu.Lock()
	firstLoad := be.loadCtx
	be.loadCtx = nil
	be.mu.Unlock()
	if firstLoad == nil || firstLoad != m.ctx || firstLoad.Value(saveContextKey{}) != "kept" {
		t.Errorf("New loaded hosts with the context %v, want the launcher's own values", firstLoad)
	}
	loading, release := make(chan context.Context, 1), make(chan struct{})
	var once sync.Once
	letGo := func() { once.Do(func() { close(release) }) }
	be.beforeUpdate = func(client.HostChange) {
		be.mu.Lock()
		loaded := be.saveCtx
		be.mu.Unlock()
		loading <- loaded
		<-release
	}
	cmd := m.saveHosts(client.Host{Name: "new", SSH: "new"}, false)
	got := make(chan tea.Msg, 1)
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); letGo(); waitSaveWorker(t, done) })
	go func() { defer close(done); got <- cmd() }()
	var loaded context.Context
	select {
	case loaded = <-loading:
	case <-time.After(10 * time.Second):
		t.Fatal("the CAS save did not reach its host-read boundary")
	}
	if loaded == nil || loaded.Value(saveContextKey{}) != "kept" {
		t.Errorf("the load lost the launcher's context values: %v", loaded)
	}
	cancel()
	if firstLoad == nil || !errors.Is(firstLoad.Err(), context.Canceled) {
		t.Error("New's host-load context did not retain its cancellation parent")
	}
	letGo()
	var msg tea.Msg
	select {
	case msg = <-got:
	case <-time.After(10 * time.Second):
		t.Fatal("the save never ended")
	}
	if saved, ok := msg.(hostSavedMsg); !ok || !errors.Is(saved.err, context.Canceled) {
		t.Errorf("the save after a cancel: %#v", msg)
	}
	be.mu.Lock()
	defer be.mu.Unlock()
	if len(be.saved) != 0 {
		t.Errorf("the launcher saved %v after the cancel", be.saved)
	}
}

// A cancel at the CAS transaction's pre-write gate still aborts the save,
// because UpdateHostsContext receives the launcher's context and values.
func TestACancelBeforeSaveHostsWritesNothing(t *testing.T) {
	be := newFakeBackend()
	be.hosts = []client.Host{{Name: "kept", SSH: "kept"}}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), saveContextKey{}, "kept"))
	defer cancel()
	m := New(ctx, be, Options{Now: func() time.Time { return now }})
	t.Cleanup(func() { m.closeAfterProgramContext(ctx) })
	be.saveGate, be.saving = make(chan struct{}), make(chan struct{})
	var once sync.Once
	letGo := func() { once.Do(func() { close(be.saveGate) }) }
	cmd := m.saveHosts(client.Host{Name: "new", SSH: "new"}, false)
	got := make(chan tea.Msg, 1)
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); letGo(); waitSaveWorker(t, done) })
	go func() { defer close(done); got <- cmd() }()
	select {
	case <-be.saving:
	case <-time.After(10 * time.Second):
		t.Fatal("the CAS save did not reach its pre-write gate")
	}
	be.mu.Lock()
	savedCtx := be.saveCtx
	be.mu.Unlock()
	if savedCtx == nil || savedCtx.Value(saveContextKey{}) != "kept" {
		t.Errorf("the save lost the launcher's context values: %v", savedCtx)
	}
	cancel()
	letGo()
	var msg tea.Msg
	select {
	case msg = <-got:
	case <-time.After(10 * time.Second):
		t.Fatal("the save never ended")
	}
	if saved, ok := msg.(hostSavedMsg); !ok || !errors.Is(saved.err, context.Canceled) {
		t.Errorf("the save after a cancel: %#v", msg)
	}
	be.mu.Lock()
	defer be.mu.Unlock()
	if len(be.saved) != 0 {
		t.Errorf("the launcher saved %v after the cancel", be.saved)
	}
	if len(be.hosts) != 1 || be.hosts[0].Name != "kept" {
		t.Errorf("the hosts changed to %v", be.hosts)
	}
}

func waitSaveWorker(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Error("the owned save worker did not end")
	}
}

// The launcher's real save writes nothing once its context has ended, as
// client.SaveHosts does.
func TestTheLaunchersSaveStopsAtACancel(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := client.SaveHostsContext(context.Background(), []client.Host{{Name: "kept", SSH: "kept"}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	host := client.Host{Name: "new", SSH: "new"}
	if _, err := (realBackend{}).UpdateHostsContext(ctx, client.HostChange{Kind: "the launcher", Now: &host}); !errors.Is(err, context.Canceled) {
		t.Errorf("the launcher's save after a cancel = %v", err)
	}
	if _, err := (realBackend{}).LoadHosts(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("the launcher's load after a cancel = %v", err)
	}
	if hosts, err := client.LoadHosts(); err != nil || len(hosts) != 1 || hosts[0].Name != "kept" {
		t.Errorf("the hosts after the cancel: %+v, %v", hosts, err)
	}
}
