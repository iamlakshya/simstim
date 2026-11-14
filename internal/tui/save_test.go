package tui

import (
	"context"
	"errors"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

type saveContextKey struct{}

// A cancel of the launcher while it loads the host list, before it saves
// the list with a host added, saves nothing: the save takes the launcher's
// context.
func TestACancelWhileTheHostListLoadsSavesNothing(t *testing.T) {
	be := newFakeBackend()
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), saveContextKey{}, "kept"))
	defer cancel()
	m := New(ctx, be, Options{Now: func() time.Time { return now }})
	be.mu.Lock()
	firstLoad := be.loadCtx
	be.loadCtx = nil
	be.mu.Unlock()
	if firstLoad != ctx {
		t.Errorf("New loaded hosts with the context %v, want the launcher's own", firstLoad)
	}
	be.loadGate, be.loading = make(chan struct{}), make(chan struct{})
	cmd := m.saveHosts(client.Host{Name: "new", SSH: "new"}, false)
	got := make(chan tea.Msg, 1)
	go func() { got <- cmd() }()
	<-be.loading // the list loads
	be.mu.Lock()
	loaded := be.loadCtx
	be.mu.Unlock()
	if loaded == nil || loaded.Value(saveContextKey{}) != "kept" {
		t.Errorf("the load lost the launcher's context values: %v", loaded)
	}
	cancel()
	close(be.loadGate)
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

// A cancel after LoadHosts returns and before SaveHosts writes still aborts
// the save, because SaveHosts receives the launcher's context.
func TestACancelBeforeSaveHostsWritesNothing(t *testing.T) {
	be := newFakeBackend()
	be.hosts = []client.Host{{Name: "kept", SSH: "kept"}}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), saveContextKey{}, "kept"))
	defer cancel()
	m := New(ctx, be, Options{Now: func() time.Time { return now }})
	be.saveGate, be.saving = make(chan struct{}), make(chan struct{})
	cmd := m.saveHosts(client.Host{Name: "new", SSH: "new"}, false)
	got := make(chan tea.Msg, 1)
	go func() { got <- cmd() }()
	<-be.saving
	be.mu.Lock()
	savedCtx := be.saveCtx
	be.mu.Unlock()
	if savedCtx == nil || savedCtx.Value(saveContextKey{}) != "kept" {
		t.Errorf("the save lost the launcher's context values: %v", savedCtx)
	}
	cancel()
	close(be.saveGate)
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
