package tui

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

type formFolderConn struct {
	Conn
	calls   atomic.Int32
	entered chan context.Context
	release chan struct{}
}

func (c *formFolderConn) Folders(ctx context.Context, _ string) (client.FoldersResult, error) {
	c.calls.Add(1)
	if c.entered != nil {
		c.entered <- ctx
		select {
		case <-ctx.Done():
		case <-c.release:
		}
	}
	if err := ctx.Err(); err != nil {
		return client.FoldersResult{}, err
	}
	return client.FoldersResult{Matches: []string{"~/src/owned/"}}, nil
}

func formFolderFixture(t *testing.T) (*harness, *hostConn, *newSession, *formFolderConn) {
	t.Helper()
	hs := sessions(t, 100, 36)
	h, _ := hs.m.selectedPair()
	if h == nil {
		t.Fatal("the form fixture has no host")
	}
	c := &formFolderConn{Conn: unclean(h.conn)}
	h.conn = cleanConn{c}
	d := newSessionDialog(hs.m, h)
	d.field = 1
	d.folder.set("~/sr")
	hs.m.modal = d
	return hs, h, d, c
}

func TestFormFoldersCancelledBeforeQueuedCommandStarts(t *testing.T) {
	for _, leave := range []string{"escape", "replacement", "dispose", "close"} {
		t.Run(leave, func(t *testing.T) {
			hs, h, d, c := formFolderFixture(t)
			cmd := hs.m.fetchFolders(d)
			ctx := d.folderCtx
			switch leave {
			case "escape":
				_, _ = hs.m.Update(keyMsg("esc"))
			case "replacement":
				_, _ = hs.m.Update(hostTierResolvedMsg{host: h.host, gen: h.gen, tier: client.TierOwn,
					next: func() tea.Cmd { hs.m.modal = &helpSheet{}; return nil }})
			case "dispose":
				_ = hs.m.disposeModal()
				hs.m.modal = nil
			case "close":
				hs.m.Close()
			}
			if ctx == nil || ctx.Err() == nil {
				t.Fatal("leaving the form kept its request live")
			}
			if msg := cmd(); msg != nil || c.calls.Load() != 0 {
				t.Fatalf("a retired folder request reached the transport: %T, %d", msg, c.calls.Load())
			}
		})
	}
}

func TestFormFoldersRequireCurrentOwnerSequenceAndHostGeneration(t *testing.T) {
	hs, h, d, c := formFolderFixture(t)
	first := hs.m.fetchFolders(d)().(foldersMsg)
	second := hs.m.fetchFolders(d)().(foldersMsg)
	_ = hs.m.onFolders(first)
	if len(d.suggest) != 0 {
		t.Fatal("an old request installed suggestions in the current form")
	}
	_ = hs.m.onFolders(second)
	if len(d.suggest) != 1 || d.suggest[0] != "~/src/owned/" || d.folderCtx != nil {
		t.Fatal("the current owned answer was not applied and retired")
	}
	d.suggest = nil
	third := hs.m.fetchFolders(d)().(foldersMsg)
	other := newSessionDialog(hs.m, h)
	other.folder.set(d.folder.value())
	hs.m.modal = other
	_ = hs.m.onFolders(third)
	if len(other.suggest) != 0 || hs.m.fetchFolders(d) != nil {
		t.Fatal("a same-host same-prefix replacement adopted another form's answer")
	}
	hs.m.modal = d
	fourth := hs.m.fetchFolders(d)().(foldersMsg)
	h.gen++
	_ = hs.m.onFolders(fourth)
	if len(d.suggest) != 0 || c.calls.Load() != 4 {
		t.Fatal("an old host generation supplied actionable folder suggestions")
	}
}

func TestFormFolderPasteRetiresOldAnswersAndBusyPasteChangesNothing(t *testing.T) {
	hs, _, d, _ := formFolderFixture(t)
	msg := hs.m.fetchFolders(d)().(foldersMsg)
	d.paste("/other")
	d.folder.set(msg.prefix)
	_ = hs.m.onFolders(msg)
	if len(d.suggest) != 0 || d.folderCtx != nil {
		t.Fatal("pasting away and back revived an old completion")
	}
	d.starting = true
	before := d.folder.value()
	d.paste("changed")
	if d.folder.value() != before {
		t.Fatal("paste changed a form whose start was already submitted")
	}
}

func TestFormFolderCloseCancelsAndJoinsStartedRead(t *testing.T) {
	hs, _, d, c := formFolderFixture(t)
	c.entered, c.release = make(chan context.Context, 1), make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(c.release) }) })
	result := make(chan tea.Msg, 1)
	cmd := hs.m.fetchFolders(d)
	go func() { result <- cmd() }()
	ctx := lifecycleWait(t, c.entered)
	closed := make(chan struct{})
	go func() { hs.m.Close(); close(closed) }()
	lifecycleWait(t, ctx.Done())
	lifecycleWait(t, closed)
	if msg := lifecycleWait(t, result); msg != nil || d.folderCtx != nil || len(d.suggest) != 0 {
		t.Fatal("a closed form retained a completion from its cancelled read")
	}
}
