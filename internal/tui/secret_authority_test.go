package tui

import (
	"context"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

type grantContextBackend struct {
	Backend
	fetches []context.Context
	host    client.Host
	id      string
}

func (b *grantContextBackend) Secrets(ctx context.Context, s client.Settings, h client.Host, names []string) (map[string]api.Secret, error) {
	b.fetches = append(b.fetches, ctx)
	return b.Backend.Secrets(ctx, s, h, names)
}

func (b *grantContextBackend) SessionSecrets(ctx context.Context, s client.Settings, h client.Host, id string, names []string) (map[string]api.Secret, error) {
	b.fetches = append(b.fetches, ctx)
	b.host, b.id = h, id
	return b.Backend.SessionSecrets(ctx, s, h, id, names)
}

type grantContextConn struct {
	Conn
	calls []context.Context
}

func (c *grantContextConn) NewSession(ctx context.Context, args client.NewArgs) (*client.Session, error) {
	c.calls = append(c.calls, ctx)
	return c.Conn.NewSession(ctx, args)
}

func (c *grantContextConn) Resume(ctx context.Context, args client.ResumeArgs) (*client.ResumeResult, error) {
	c.calls = append(c.calls, ctx)
	return c.Conn.Resume(ctx, args)
}

func TestStartSecretRequestSurvivesOnlyItsOwnFolderRetry(t *testing.T) {
	hs := folderLab(t)
	h := hs.m.hostByName("atlas-dev")
	backend := &grantContextBackend{Backend: hs.be}
	connection := &grantContextConn{Conn: h.conn}
	hs.m.be, h.conn = backend, connection
	hs.be.secrets = map[string]api.Secret{}
	request := startReq{args: client.NewArgs{Agent: "shell", Cwd: "~/src/new-app"}}
	first, ok := startResult(t, hs.m, hs.m.startSessionReady(h, request))
	if !ok {
		t.Fatal("first actual start lost its result")
	}
	if _, asks := client.FolderQuestionOf(first.err); !asks {
		t.Fatalf("first start did not ask about its missing folder: %v", first.err)
	}
	if first.grants == nil || len(backend.fetches) != 1 || len(connection.calls) != 1 || backend.fetches[0] != connection.calls[0] {
		t.Fatal("prefetch and first start did not share their actual request context")
	}
	hs.m.grantOwners.mu.Lock()
	_, held := hs.m.grantOwners.held[first.grants]
	hs.m.grantOwners.mu.Unlock()
	if !held {
		t.Fatal("the folder question lost its original input owner")
	}
	if connection.calls[0].Err() != context.Canceled {
		t.Fatal("completed first start kept its context alive")
	}
	retry := first.startReq
	retry.args.MakeFolder = true
	second, ok := startResult(t, hs.m, hs.m.startSessionReady(h, retry))
	if !ok || second.err != nil || second.s == nil || second.grants != first.grants {
		t.Fatalf("folder retry lost its original request: %+v", second)
	}
	if len(backend.fetches) != 1 || len(connection.calls) != 2 {
		t.Fatal("folder retry fetched values again or omitted the actual start")
	}
	hs.m.grantOwners.mu.Lock()
	_, held = hs.m.grantOwners.held[first.grants]
	hs.m.grantOwners.mu.Unlock()
	if held {
		t.Fatal("the completed logical start retained its input owner")
	}
	fresh, ok := startResult(t, hs.m, hs.m.startSessionReady(h, request))
	if !ok || fresh.grants == nil || fresh.grants == first.grants || len(backend.fetches) != 2 {
		t.Fatal("another logical start reused the previous request or values")
	}
}

func TestResumeSecretFetchKeepsActualHostSessionAndContext(t *testing.T) {
	hs := endedLab(t, 80, 24)
	h, selected := hs.m.selectedPair()
	if h == nil || selected == nil {
		t.Fatal("the fixture has no selected ended session")
	}
	id := selected.ID
	h.host.Secrets, selected.Secrets = []string{"TOKEN"}, []string{"TOKEN"}
	hs.be.secrets = map[string]api.Secret{"TOKEN": "fixture-value"}
	backend := &grantContextBackend{Backend: hs.be}
	connection := &grantContextConn{Conn: h.conn}
	hs.m.be, h.conn = backend, connection
	hs.key("u")
	if len(backend.fetches) != 1 || len(connection.calls) != 1 || backend.fetches[0] != connection.calls[0] {
		t.Fatal("resume prefetch and final operation did not share their actual context")
	}
	if backend.id != id || !client.SameEntry(backend.host, h.host) {
		t.Fatalf("resume fetched for a different saved host or session: %q/%q", backend.host.Name, backend.id)
	}
	if connection.calls[0].Err() != context.Canceled {
		t.Fatal("completed resume kept its context alive")
	}
}

func TestProgramPauseReleasesADroppedStartQuestion(t *testing.T) {
	hs := folderLab(t)
	host := hs.m.hostByName("atlas-dev")
	result, ok := startResult(t, hs.m, hs.m.startSessionReady(host, startReq{args: client.NewArgs{Agent: "shell", Cwd: "~/src/new-app"}}))
	if !ok {
		t.Fatal("the real start produced no result")
	}
	if _, asks := client.FolderQuestionOf(result.err); !asks || result.grants == nil {
		t.Fatalf("the fixture made no owned folder question: %v", result.err)
	}
	// Do not deliver the result to Update. The terminal handoff must still
	// own and release it after joining the original background operations.
	hs.m.pauseBg()
	hs.m.grantOwners.mu.Lock()
	held := len(hs.m.grantOwners.held)
	hs.m.grantOwners.mu.Unlock()
	if held != 0 || hs.m.grantOwners.pendingError() != nil {
		t.Fatalf("a dropped question retained inputs after the join: held=%d err=%v", held, hs.m.grantOwners.pendingError())
	}
}
