package host_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/paths"
)

// slowPendingRestore starts a session named "a", crashes its holder, and
// runs Restore while the restored holder child is stalled between
// LoadManifest and listen. When it returns, Restore has given up waiting
// and released op.lock, while the restored holder process still runs and
// holds holder.lock without a socket.
func slowPendingRestore(t *testing.T) (id, stall string, firstPID int) {
	t.Helper()
	hosttest.New(t)
	observeHolders(t)
	s := start(t, "a", "sleep", "60")
	crash(t, s.ID)
	m := manifest(t, s.ID)
	m.Run.BootID = "a boot before"
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	oldStart, oldRestore, oldLate := *host.StartWait, *host.RestoreWait, *host.LateStart
	t.Cleanup(func() { *host.StartWait, *host.RestoreWait, *host.LateStart = oldStart, oldRestore, oldLate })
	*host.StartWait = 150 * time.Millisecond
	*host.RestoreWait = 150 * time.Millisecond
	*host.LateStart = func(string) {}

	stall = filepath.Join(t.TempDir(), "start-stall")
	if err := os.WriteFile(stall, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(stall) })
	t.Setenv(host.StartStallEnv, stall)

	res := host.Restore(false)
	if len(res) != 1 || res[0].Action != "failed" || !strings.Contains(res[0].Why, "and its holder may still start") {
		t.Fatalf("slow restore: %+v", res)
	}
	hosttest.WaitFor(t, 5*time.Second, "the restored holder to load the manifest and stall", func() bool {
		b, err := os.ReadFile(stall + ".stalled")
		return err == nil && string(b) == s.ID
	})
	pids := holderPIDs(t, s.ID)
	if len(pids) != 1 || !host.HolderLockHeld(s.ID) {
		t.Fatalf("pending holder pids = %v, lock held = %v", pids, host.HolderLockHeld(s.ID))
	}
	return s.ID, stall, pids[0]
}

// Ownership across a slow restored holder whose socket is not open yet
// (decision 10, item 3 and addendum A1): an acknowledged rename survives
// the delayed holder's first and tick saves, Delete stops the pending
// holder before removing the folder, a second Restore skips the pending
// holder instead of spawning a second one, and Resume stops the pending
// holder before starting its own.
func TestSlowRestoredHolderPendingOwnership(t *testing.T) {
	t.Run("rename survives slow restore", func(t *testing.T) {
		id, stall, _ := slowPendingRestore(t)
		if err := host.Rename(id, "b"); err != nil {
			t.Fatalf("rename while restored holder is pending: %v", err)
		}
		if m := manifest(t, id); m.Name != "b" {
			t.Fatalf("manifest right after rename: %q, want b", m.Name)
		}
		if err := os.Remove(stall); err != nil {
			t.Fatal(err)
		}
		waitState(t, id, host.StateWorking)
		if m := manifest(t, id); m.Name != "b" {
			t.Fatalf("manifest after slow holder's first save: %q, want b", m.Name)
		}
		// Wait past a 1 s holder tick so a tick save runs too.
		time.Sleep(1200 * time.Millisecond)
		if _, err := host.DeleteNamed(id, "a"); codeOf(err) != "session_renamed" || !strings.Contains(err.Error(), `now named "b"`) {
			t.Errorf("delete under old name after tick: %v", err)
		}
		pid := hosttest.HolderPID(t, id)
		_, _ = host.HolderRequest(id, "kill", nil, 3*time.Second)
		if err := hosttest.Reap(pid, 10*time.Second); err != nil {
			t.Fatal(err)
		}
		if m := manifest(t, id); m.Name != "b" {
			t.Errorf("manifest after holder's final save: %q, want b", m.Name)
		}
	})

	t.Run("delete stops pending holder", func(t *testing.T) {
		id, stall, firstPID := slowPendingRestore(t)
		res, err := host.DeleteNamed(id, "a")
		if err != nil || !res.Deleted {
			t.Fatalf("DeleteNamed while restored holder is pending: %+v, %v", res, err)
		}
		if err := hosttest.Reap(firstPID, 2*time.Second); err != nil {
			t.Fatalf("pending holder was not stopped by delete: %v", err)
		}
		if host.HolderLockHeld(id) || holderRuns(t, id) {
			t.Fatalf("holder lock %v or process %v still live after delete", host.HolderLockHeld(id), holderPIDs(t, id))
		}
		_ = os.Remove(stall)
		time.Sleep(150 * time.Millisecond)
		if _, err := os.Stat(paths.SessionDir(id)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("session folder after delete: %v", err)
		}
	})

	t.Run("second restore skips pending holder", func(t *testing.T) {
		id, stall, firstPID := slowPendingRestore(t)
		if res2 := host.Restore(true); len(res2) != 0 {
			t.Fatalf("second restore while first holder is pending: %+v, want empty (skipped as live)", res2)
		}
		if pids := holderPIDs(t, id); len(pids) != 1 || pids[0] != firstPID {
			t.Fatalf("holders after second restore = %v, want [%d]", pids, firstPID)
		}
		if err := os.Remove(stall); err != nil {
			t.Fatal(err)
		}
		waitState(t, id, host.StateWorking)
		if pids := holderPIDs(t, id); len(pids) != 1 || pids[0] != firstPID {
			t.Errorf("holders once started = %v, want [%d]", pids, firstPID)
		}
	})

	t.Run("resume stops pending holder first", func(t *testing.T) {
		id, _, firstPID := slowPendingRestore(t)
		t.Setenv(host.StartStallEnv, "")
		if _, err := host.Resume(api.ResumeArgs{ID: id}); err != nil {
			t.Fatalf("resume while restored holder is pending: %v", err)
		}
		if err := hosttest.Reap(firstPID, 2*time.Second); err != nil {
			t.Fatalf("first pending holder still alive after resume: %v", err)
		}
		if !errors.Is(syscall.Kill(firstPID, 0), syscall.ESRCH) {
			t.Fatalf("first holder pid %d still exists after resume", firstPID)
		}
		waitState(t, id, host.StateWorking)
		pids := holderPIDs(t, id)
		if len(pids) != 1 || pids[0] == firstPID {
			t.Errorf("live holders after resume = %v, want 1 new holder (!= %d)", pids, firstPID)
		}
	})

	t.Run("persist failure stops pending holder", func(t *testing.T) {
		hosttest.New(t)
		observeHolders(t)
		s := start(t, "a", "sleep", "60")
		crash(t, s.ID)
		m := manifest(t, s.ID)
		m.Run.BootID = "a boot before"
		if err := m.Save(); err != nil {
			t.Fatal(err)
		}
		oldStart, oldRestore, oldLate := *host.StartWait, *host.RestoreWait, *host.LateStart
		oldWritePending := *host.WritePendingManifest
		t.Cleanup(func() {
			*host.StartWait, *host.RestoreWait, *host.LateStart = oldStart, oldRestore, oldLate
			*host.WritePendingManifest = oldWritePending
		})
		*host.StartWait = 150 * time.Millisecond
		*host.RestoreWait = 150 * time.Millisecond
		*host.LateStart = func(string) {}
		*host.WritePendingManifest = func(*host.Manifest) error {
			return errors.New("injected pending save failure")
		}

		stall := filepath.Join(t.TempDir(), "start-stall")
		if err := os.WriteFile(stall, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Remove(stall) })
		t.Setenv(host.StartStallEnv, stall)

		res := host.Restore(false)
		if len(res) != 1 || res[0].Action != "failed" || !strings.Contains(res[0].Why, "holder was stopped") {
			t.Fatalf("restore on persistPendingHolder failure: %+v", res)
		}
		if host.HolderLockHeld(s.ID) || len(holderPIDs(t, s.ID)) != 0 {
			t.Fatalf("unrecorded pending holder left running: lock=%v pids=%v", host.HolderLockHeld(s.ID), holderPIDs(t, s.ID))
		}

		// Restore normal startup and verify Resume and DeleteNamed both succeed.
		t.Setenv(host.StartStallEnv, "")
		*host.StartWait = oldStart
		*host.RestoreWait = oldRestore
		*host.WritePendingManifest = oldWritePending

		if _, err := host.Resume(api.ResumeArgs{ID: s.ID}); err != nil {
			t.Fatalf("Resume after stopped pending restore: %v", err)
		}
		waitState(t, s.ID, host.StateWorking)
		dres, err := host.DeleteNamed(s.ID, "a")
		if err != nil || !dres.Deleted {
			t.Fatalf("DeleteNamed after Resume: %+v, %v", dres, err)
		}
	})
}
