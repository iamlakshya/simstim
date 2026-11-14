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
	first, ok := hs.m.startSessionReady(h, request)().(startedMsg)
	if !ok {
		t.Fatal("first actual start lost its result")
	}
	if _, asks := client.FolderQuestionOf(first.err); !asks {
		t.Fatalf("first start did not ask about its missing folder: %v", first.err)
	}
	if first.grants == nil || len(backend.fetches) != 1 || len(connection.calls) != 1 || backend.fetches[0] != connection.calls[0] {
		t.Fatal("prefetch and first start did not share their actual request context")
	}
	if connection.calls[0].Err() != context.Canceled {
		t.Fatal("completed first start kept its context alive")
	}
	retry := first.startReq
	retry.args.MakeFolder = true
	second, ok := hs.m.startSessionReady(h, retry)().(startedMsg)
	if !ok || second.err != nil || second.s == nil || second.grants != first.grants {
		t.Fatalf("folder retry lost its original request: %+v", second)
	}
	if len(backend.fetches) != 1 || len(connection.calls) != 2 {
		t.Fatal("folder retry fetched values again or omitted the actual start")
	}
	fresh, ok := hs.m.startSessionReady(h, request)().(startedMsg)
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
