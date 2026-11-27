package host_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestResumeRunningLegacyUsesSettledName(t *testing.T) {
	hosttest.New(t)
	stoppedSession(t)
	if err := os.WriteFile(filepath.Join(paths.SessionDir(fakeID), "rename.unsaved"), []byte(`{"name":"after"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	serveClosableLegacyHolder(t, func(int, proto.Request) *proto.Response {
		t.Error("running resume sent an operation to the holder")
		return ok
	})
	_, err := host.Resume(api.ResumeArgs{ID: fakeID})
	if codeOf(err) != "agent_running" || !strings.Contains(err.Error(), "the agent of after still runs") {
		t.Fatalf("running resume used stale name: %v", err)
	}
}

func TestResumeReconcilesCrashedLegacyRename(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		for _, request := range []int64{256 << 20, -1, 0} {
			t.Run(fmt.Sprintf("uncertain=%t/memory=%d", uncertain, request), func(t *testing.T) {
				checkResumeRenameOptions(t, uncertain, request)
			})
		}
	}
}

func checkResumeRenameOptions(t *testing.T, uncertain bool, request int64) {
	t.Helper()
	hosttest.New(t)
	m := &host.Manifest{
		Schema: 1, ID: fakeID, Name: "before", Cwd: t.TempDir(),
		Agent: host.AgentInfo{Kind: "shell", Command: []string{"/bin/sh", "-c", "exec sleep 60"}},
		Term:  host.TermInfo{Cols: 80, Rows: 24}, Status: host.Status{State: host.StateExited},
		Limits: host.Limits{MemoryAsked: 512 << 20}, Run: host.Runtime{RespawnCount: 3},
	}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	unsaved := filepath.Join(paths.SessionDir(fakeID), "rename.unsaved")
	if err := os.WriteFile(unsaved, []byte(fmt.Sprintf(`{"name":"after","uncertain":%t}`, uncertain)), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := host.Resume(api.ResumeArgs{ID: fakeID, MemoryMax: request})
	if err != nil {
		t.Fatal(err)
	}
	wantName := "after"
	if uncertain {
		wantName = "before"
	}
	if res.Session.Name != wantName {
		t.Fatalf("resumed name = %q, want %q", res.Session.Name, wantName)
	}
	disk, err := host.LoadManifest(fakeID)
	if err != nil || disk.Name != wantName {
		t.Fatalf("resumed manifest = %+v, %v", disk, err)
	}
	wantMemory := int64(512 << 20)
	switch {
	case request > 0:
		wantMemory = request
	case request < 0:
		wantMemory = 0
	}
	if disk.Limits.MemoryAsked != wantMemory || disk.Run.RespawnCount != 0 {
		t.Fatalf("reconciliation lost resume options: memory=%d restores=%d", disk.Limits.MemoryAsked, disk.Run.RespawnCount)
	}
	if uncertain {
		_, stillUncertain := readUnsavedRecord(t)
		if !stillUncertain {
			t.Fatal("resume cleared uncertain rename")
		}
		for _, name := range []string{"before", "after"} {
			if res, err := host.DeleteNamed(fakeID, name); codeOf(err) != "session_renamed" || res.Deleted {
				t.Fatalf("uncertain delete %q = %+v, %v", name, res, err)
			}
		}
		if err := host.Rename(fakeID, "confirmed"); err != nil {
			t.Fatal(err)
		}
		if res, err := host.DeleteNamed(fakeID, "confirmed"); err != nil || !res.Deleted {
			t.Fatalf("confirmed rename delete = %+v, %v", res, err)
		}
	} else if _, err := os.Stat(unsaved); !os.IsNotExist(err) {
		t.Fatalf("settled record remains: %v", err)
	}
}

func TestRestoreReconciliationKeepsAttemptIncrement(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(map[bool]string{false: "settled", true: "uncertain"}[uncertain], func(t *testing.T) { checkRestoreReconcileCount(t, uncertain) })
	}
}

func checkRestoreReconcileCount(t *testing.T, uncertain bool) {
	t.Helper()
	hosttest.New(t)
	m := &host.Manifest{
		Schema: 1, ID: fakeID, Name: "before", Cwd: t.TempDir(),
		Agent: host.AgentInfo{Kind: "shell", Command: []string{"/bin/sh", "-c", "exec sleep 60"}},
		Term:  host.TermInfo{Cols: 80, Rows: 24}, Status: host.Status{State: host.StateLost},
		Restart: host.Restart{Policy: "resume", MaxAttempts: 4}, Run: host.Runtime{RespawnCount: 1},
	}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.SessionDir(fakeID), "rename.unsaved"), []byte(fmt.Sprintf(`{"name":"after","uncertain":%t}`, uncertain)), 0o600); err != nil {
		t.Fatal(err)
	}
	results := host.Restore(true)
	if len(results) != 1 || results[0].Action != "restored" {
		t.Fatalf("restore = %+v", results)
	}
	disk, err := host.LoadManifest(fakeID)
	wantName := "after"
	if uncertain {
		wantName = "before"
	}
	if err != nil || disk.Name != wantName || disk.Run.RespawnCount != 2 {
		t.Fatalf("restored manifest = %+v, %v", disk, err)
	}
}

func TestNewReservesEarlierSettledLegacyRenameCandidate(t *testing.T) {
	hosttest.New(t)
	stoppedSession(t)
	stale, err := host.LoadManifest(fakeID)
	if err != nil {
		t.Fatal(err)
	}
	const candidate = "frontend"
	var calls atomic.Int32
	serveClosableLegacyHolder(t, func(_ int, req proto.Request) *proto.Response {
		if req.Op != "rename" {
			return ok
		}
		if calls.Add(1) != 1 {
			return nil
		}
		m, err := host.LoadManifest(fakeID)
		if err != nil {
			t.Error(err)
			return nil
		}
		m.Name = candidate
		if err := m.Save(); err != nil {
			t.Error(err)
		}
		return ok
	})
	if err := host.Rename(fakeID, candidate); err != nil {
		t.Fatal(err)
	}
	if err := stale.Save(); err != nil {
		t.Fatal(err)
	}
	oldWait := *host.RenameWait
	t.Cleanup(func() { *host.RenameWait = oldWait })
	*host.RenameWait = -4850 * time.Millisecond
	if err := host.Rename(fakeID, "later"); codeOf(err) != "busy" {
		t.Fatalf("unanswered later rename = %v", err)
	}
	*host.RenameWait = oldWait
	oldDraw := *host.DrawID
	t.Cleanup(func() { *host.DrawID = oldDraw })
	var draws atomic.Int32
	*host.DrawID = func() string {
		if draws.Add(1) == 1 {
			return candidate
		}
		return "zzzz2345"
	}
	s, err := host.New(host.NewArgs{Name: "fresh", Cwd: "~", Argv: []string{"sleep", "60"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "zzzz2345" {
		t.Fatalf("new session used old rename candidate %q", s.ID)
	}
}
