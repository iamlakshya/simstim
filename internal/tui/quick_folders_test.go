package tui

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

type quickFolderConn struct {
	Conn
	calls   atomic.Int32
	entered chan context.Context
	release chan struct{}
}

func (c *quickFolderConn) Folders(ctx context.Context, _ string) (client.FoldersResult, error) {
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
	return client.FoldersResult{Matches: []string{"~/src/owned/", "~/src/unsafe\u200cname/"}}, nil
}

func quickFolderFixture(t *testing.T) (*harness, *hostConn, *promptState, *quickFolderConn) {
	t.Helper()
	hs := home(t, 100, 36)
	h := hs.m.newSessionHost()
	if h == nil || !h.online() {
		t.Fatal("the quick-folder fixture has no online host")
	}
	c := &quickFolderConn{Conn: unclean(h.conn)}
	h.conn = cleanConn{c}
	p := &promptState{mode: promptNew, host: h.name(), in: newInput("shell ~/sr", "")}
	hs.m.prompt = p
	return hs, h, p, c
}

func TestQuickFoldersRejectWrongModeAndUninstalledOwner(t *testing.T) {
	hs, _, p, c := quickFolderFixture(t)
	p.mode = promptSearch
	if hs.m.askFolders() != nil || p.folderCtx != nil || c.calls.Load() != 0 {
		t.Fatal("search mode created a folder request")
	}
	p.mode = promptNew
	hs.m.prompt = &promptState{mode: promptNew, host: p.host, in: newInput(p.in.value(), "")}
	if hs.m.askQuickFolders(p) != nil || p.folderCtx != nil || c.calls.Load() != 0 {
		t.Fatal("an uninstalled same-host prompt created a folder request")
	}
}

func TestQuickFoldersCancelledBeforeQueuedCommandStarts(t *testing.T) {
	for _, leave := range []string{"escape", "replacement", "reopen", "paste", "start", "handoff", "close"} {
		t.Run(leave, func(t *testing.T) {
			hs, h, p, c := quickFolderFixture(t)
			cmd := hs.m.askFolders()
			ctx := p.folderCtx
			switch leave {
			case "escape":
				_, _ = hs.m.Update(keyMsg("esc"))
			case "replacement":
				_, _ = hs.m.Update(hostTierResolvedMsg{host: h.host, gen: h.gen, tier: client.TierOwn,
					next: func() tea.Cmd {
						hs.m.prompt = &promptState{mode: promptNew, host: p.host, in: newInput(p.in.value(), "")}
						return nil
					}})
			case "reopen":
				_ = hs.m.openQuickStart()
				hs.m.prompt.in.set(p.in.value())
			case "paste":
				_, _ = hs.m.Update(tea.PasteMsg{Content: "/other"})
			case "start":
				_, _ = hs.m.Update(keyMsg("enter"))
			case "handoff":
				hs.m.pauseBg()
				hs.m.openBg(hs.m.ctx)
			case "close":
				hs.m.Close()
			}
			if ctx == nil || ctx.Err() == nil {
				t.Fatal("the retired prompt request context is still live")
			}
			if msg := cmd(); msg != nil || c.calls.Load() != 0 {
				t.Fatalf("a retired quick request reached the transport: %T, %d", msg, c.calls.Load())
			}
		})
	}
}

func TestQuickFolderRepliesKeepCurrentOwnerSequencePrefixAndGeneration(t *testing.T) {
	hs, h, p, c := quickFolderFixture(t)
	first := hs.m.askFolders()().(foldersMsg)
	p.in.set("shell ~/other")
	middle := hs.m.askFolders()
	p.in.set("shell ~/sr")
	current := hs.m.askFolders()().(foldersMsg)
	if middle() != nil {
		t.Fatal("a superseded middle request still ran")
	}
	_ = hs.m.onFolders(first)
	if len(p.folders) != 0 {
		t.Fatal("a same-prefix old sequence supplied completions")
	}
	_ = hs.m.onFolders(current)
	if len(p.folders) != 1 || p.folders[0] != "~/src/owned/" || p.folderCtx != nil {
		t.Fatalf("the current reply lost raw-path filtering or retirement: %#v", p.folders)
	}
	if c.calls.Load() != 2 {
		t.Fatal("a cancelled request executed or the valid request was omitted")
	}
	p.stopFolders()
	late := hs.m.askFolders()().(foldersMsg)
	replacement := &promptState{mode: promptNew, host: p.host, in: newInput(p.in.value(), "")}
	hs.m.prompt = replacement
	_ = hs.m.onFolders(late)
	if len(replacement.folders) != 0 {
		t.Fatal("a reopened same-prefix prompt adopted another owner's reply")
	}
	hs.m.prompt = p
	p.stopFolders()
	oldPrefix := hs.m.askFolders()().(foldersMsg)
	p.in.set("shell ~/different")
	_ = hs.m.onFolders(oldPrefix)
	if len(p.folders) != 0 {
		t.Fatal("a reply for an old input prefix supplied completions")
	}
	p.in.set("shell ~/sr")
	p.stopFolders()
	oldGeneration := hs.m.askFolders()().(foldersMsg)
	h.gen++
	_ = hs.m.onFolders(oldGeneration)
	if len(p.folders) != 0 {
		t.Fatal("an old connection generation supplied actionable paths")
	}
}

func TestQuickFolderCacheCannotCrossHandoff(t *testing.T) {
	hs, _, p, c := quickFolderFixture(t)
	_ = hs.m.onFolders(hs.m.askFolders()().(foldersMsg))
	if hs.m.ghost() == "" || len(p.folders) != 1 {
		t.Fatal("the valid cache was not usable before handoff")
	}
	hs.m.pauseBg()
	hs.m.openBg(hs.m.ctx)
	if hs.m.ghost() != "" || c.calls.Load() != 1 {
		t.Fatal("handoff reused an old cache or automatically queried the host")
	}
	_, _ = hs.m.Update(nil)
	if len(p.folders) != 0 {
		t.Fatal("the next model update retained a retired program's cache")
	}
}

func TestQuickFolderCloseCancelsAndJoinsTheExistingFiveSecondRead(t *testing.T) {
	hs, _, p, c := quickFolderFixture(t)
	c.entered, c.release = make(chan context.Context, 1), make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(c.release) }) })
	cmd := hs.m.askFolders()
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	ctx := lifecycleWait(t, c.entered)
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 5*time.Second {
		t.Fatal("the existing five-second completion bound was lost")
	}
	closed := make(chan struct{})
	go func() { hs.m.Close(); close(closed) }()
	lifecycleWait(t, ctx.Done())
	lifecycleWait(t, closed)
	if msg := lifecycleWait(t, result); msg != nil || p.folderCtx != nil || len(p.folders) != 0 {
		t.Fatal("Close retained a cancelled quick-folder answer")
	}
}
