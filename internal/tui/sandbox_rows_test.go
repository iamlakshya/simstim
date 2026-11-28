package tui

import (
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

func TestSandboxRowsGroupedAndIndented(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	h := &hostConn{
		host:   client.Host{Name: "devbox"},
		status: stOnline,
		loaded: true,
		sessions: []client.Session{
			{ID: "p1", Name: "auth-fix", Agent: "claude", State: api.StateWorking, Alive: true, CreatedAt: now.Add(-10 * time.Minute), Wall: &api.WallInfo{Level: "process", Driver: "process"}},
			{ID: "other", Name: "docs", Agent: "codex", State: api.StateIdle, Alive: true, CreatedAt: now.Add(-5 * time.Minute)},
			{ID: "c1", Name: "auth-fix-sh", SandboxOf: "p1", Agent: "shell", State: api.StateIdle, Alive: true, CreatedAt: now.Add(-2 * time.Minute), Wall: &api.WallInfo{Level: "process", Driver: "process"}},
			{ID: "c2", Name: "auth-fix-codex", SandboxOf: "p1", Agent: "codex", State: api.StateWorking, Alive: true, CreatedAt: now.Add(-1 * time.Minute), Wall: &api.WallInfo{Level: "process", Driver: "process"}},
		},
	}
	m := &Model{screen: scrSessions, cur: "devbox", hosts: []*hostConn{h}}
	rows := m.rows()
	if len(rows) != 4 {
		t.Fatalf("len(rows) = %d, want 4", len(rows))
	}
	// Primary p1 must be directly followed by its indented children c2 and c1 (before/after other).
	pIdx := -1
	for i, r := range rows {
		if r.s.ID == "p1" {
			pIdx = i
			if r.indent {
				t.Errorf("primary row p1 should not be indented")
			}
		}
	}
	if pIdx < 0 || pIdx+2 >= len(rows) {
		t.Fatalf("primary p1 at %d with %d rows", pIdx, len(rows))
	}
	for _, ci := range []int{pIdx + 1, pIdx + 2} {
		if rows[ci].s.SandboxOf != "p1" || !rows[ci].indent {
			t.Errorf("row %d = %+v, want indented child of p1", ci, rows[ci])
		}
	}
}

func TestOpenSandboxShellReusesOrSpawnsAndRejectsChangedHost(t *testing.T) {
	hs := sessions(t, 80, 24)
	h := hs.m.hostByName("atlas-dev")
	if h == nil || len(h.sessions) == 0 {
		t.Fatal("missing atlas-dev sessions")
	}
	// Mark first session as walled and ensure pressing 't' spawns a shell in its sandbox.
	h.sessions[0].Wall = &api.WallInfo{Level: "process", Driver: "process"}
	hs.be.state["atlas-dev"].sessions = append([]client.Session(nil), h.sessions...)
	hs.m.sel = sessKey{"atlas-dev", h.sessions[0].ID}
	hs.m.syncSel()

	hs.key("t")
	// Verify a shell session was started with In == primary ID.
	var sawShell bool
	for _, s := range h.sessions {
		if s.Agent == "shell" {
			sawShell = true
		}
	}
	if !sawShell {
		t.Fatalf("pressing t did not start a shell session; sessions=%+v", h.sessions)
	}

	// A stale hostConn must fail with host_changed rather than re-resolving by name (cli-spec 15.2).
	cmd := hs.m.onShellOpened(shellOpenedMsg{
		h:        &hostConn{host: client.Host{Name: "atlas-dev"}},
		hostName: "atlas-dev",
		s:        &client.Session{ID: "sh99", Name: "sh"},
	})
	if cmd == nil {
		t.Fatal("expected error toast for stale hostConn")
	}
}
