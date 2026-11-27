package host_test

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// When an older holder crashes after a delayed stale save overwrote
// manifest.json back to "a", a settled rename.unsaved ("b") reconciles into
// manifest.json before rename.unsaved goes: DeleteNamed("a") is refused as
// renamed to "b", List and Restore preserve "b", and DeleteNamed("b")
// succeeds. An uncertain rename.unsaved stays fail-closed even after the
// older holder dies until an explicit Rename clears it (decision 11 item 1).
func TestLegacyHolderCrashReconcilesSettledRename(t *testing.T) {
	t.Run("settled rename reconciles on delete list and restore", func(t *testing.T) {
		hosttest.New(t)
		stoppedSession(t)
		stale, err := host.LoadManifest(fakeID)
		if err != nil {
			t.Fatal(err)
		}
		stale.Status.State = host.StateExited
		ln := serveClosableLegacyHolder(t, func(n int, _ proto.Request) *proto.Response {
			if n == 1 {
				go func() {
					if m, err := host.LoadManifest(fakeID); err == nil {
						m.Name = "b"
						_ = m.Save()
					}
				}()
			}
			return ok
		})
		if err := host.Rename(fakeID, "b"); err != nil {
			t.Fatalf("rename to b: %v", err)
		}
		// Overwrite manifest.json with stale "a" and kill the legacy socket
		// without a final save.
		if err := stale.Save(); err != nil {
			t.Fatal(err)
		}
		_ = ln.Close()
		_ = os.Remove(paths.Socket(fakeID))

		if res, err := host.DeleteNamed(fakeID, "a"); codeOf(err) != "session_renamed" || !strings.Contains(err.Error(), `now named "b"`) || res.Deleted {
			t.Fatalf("DeleteNamed(\"a\") after legacy crash: %+v, %v", res, err)
		}
		if m, err := host.LoadManifest(fakeID); err != nil || m.Name != "b" {
			t.Fatalf("manifest after reconciliation = %+v, %v, want Name=\"b\"", m, err)
		}
		list, err := host.List()
		if err != nil || len(list) != 1 || list[0].Name != "b" {
			t.Fatalf("List() after reconciliation = %+v, %v, want Name=\"b\"", list, err)
		}

		// Also verify Restore reconciles a stale manifest before skipping or starting.
		if err := os.WriteFile(filepath.Join(paths.SessionDir(fakeID), "rename.unsaved"),
			[]byte(`{"name":"c","before":"2020-01-01T00:00:00Z"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		rres := host.Restore(false)
		if len(rres) != 1 || rres[0].Name != "c" {
			t.Fatalf("Restore() result = %+v, want reconciled Name=\"c\"", rres)
		}
		if m, err := host.LoadManifest(fakeID); err != nil || m.Name != "c" {
			t.Fatalf("manifest after Restore reconciliation = %+v, %v, want Name=\"c\"", m, err)
		}
		if res, err := host.DeleteNamed(fakeID, "c"); err != nil || !res.Deleted {
			t.Fatalf("DeleteNamed(\"c\") after reconciliation: %+v, %v", res, err)
		}
	})

	t.Run("crashed uncertain rename stays fail-closed until explicit rename", func(t *testing.T) {
		hosttest.New(t)
		stoppedSession(t)
		ln := serveClosableLegacyHolder(t, func(int, proto.Request) *proto.Response {
			if m, err := host.LoadManifest(fakeID); err == nil {
				m.Name = "b"
				_ = m.Save()
			}
			return nil
		})
		if err := host.Rename(fakeID, "b"); codeOf(err) != "busy" {
			t.Fatalf("uncertain rename: %v", err)
		}
		_ = ln.Close()
		_ = os.Remove(paths.Socket(fakeID))

		for _, name := range []string{"a", "b"} {
			if res, err := host.DeleteNamed(fakeID, name); codeOf(err) != "session_renamed" || res.Deleted {
				t.Fatalf("DeleteNamed(%q) after uncertain crash: %+v, %v", name, res, err)
			}
		}
		if err := host.Rename(fakeID, "c"); err != nil {
			t.Fatalf("explicit stopped Rename after crash: %v", err)
		}
		if res, err := host.DeleteNamed(fakeID, "c"); err != nil || !res.Deleted {
			t.Fatalf("DeleteNamed(\"c\") after explicit Rename: %+v, %v", res, err)
		}
	})
}

// Once a rename to a running older holder times out without a clear reply,
// a subsequent rename on that running older holder is refused with busy so
// out-of-order completion of the first request cannot clear Uncertain or
// allow deletion under a mismatched name (decision 11 item 2).
func TestLegacyRenameTimedOutRequestBlocksLaterRenameAndOutOfOrderSave(t *testing.T) {
	hosttest.New(t)
	stoppedSession(t)
	oldWait := *host.RenameWait
	t.Cleanup(func() { *host.RenameWait = oldWait })
	*host.RenameWait = -4850 * time.Millisecond

	unblockFirst := make(chan struct{})
	firstSaved := make(chan struct{})
	var requests atomic.Int32
	serveClosableLegacyHolder(t, func(n int, _ proto.Request) *proto.Response {
		requests.Add(1)
		if n == 1 {
			<-unblockFirst
			if m, err := host.LoadManifest(fakeID); err == nil {
				m.Name = "b"
				_ = m.Save()
			}
			close(firstSaved)
			return ok
		}
		if m, err := host.LoadManifest(fakeID); err == nil {
			m.Name = "c"
			_ = m.Save()
		}
		return ok
	})

	if err := host.Rename(fakeID, "b"); codeOf(err) != "busy" {
		t.Fatalf("first timed-out rename to b: %v", err)
	}
	*host.RenameWait = oldWait

	// A second rename to c while the legacy holder still has the uncertain
	// request in flight must be refused before reaching the socket.
	if err := host.Rename(fakeID, "c"); codeOf(err) != "busy" || hintOf(err) == "" {
		t.Fatalf("second rename to c after uncertain timeout: %v (hint %q)", err, hintOf(err))
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("legacy holder received %d requests, want 1", got)
	}

	// Let the paused first request finish out of order and save "b".
	close(unblockFirst)
	<-firstSaved

	for _, name := range []string{"a", "b", "c"} {
		if res, err := host.DeleteNamed(fakeID, name); codeOf(err) != "session_renamed" || res.Deleted {
			t.Errorf("DeleteNamed(%q) after out-of-order completion: %+v, %v", name, res, err)
		}
	}
}

// A missing or unlinked socket does not clear rename.unsaved while the
// older holder's recorded process still lives (decision 11 addendum A1).
func TestLegacySocketGoneWhileProcessLivesFailsClosed(t *testing.T) {
	hosttest.New(t)
	cmd := legacyHolderProcess(t)
	reaped := false
	t.Cleanup(func() {
		if !reaped {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})

	stale := &host.Manifest{
		Schema: 1,
		ID:     fakeID,
		Name:   "a",
		Run:    host.Runtime{HolderPID: cmd.Process.Pid, BootID: host.BootID()},
	}
	if err := stale.Save(); err != nil {
		t.Fatal(err)
	}

	ln := serveClosableLegacyHolder(t, func(n int, _ proto.Request) *proto.Response {
		if n == 1 {
			go func() {
				if m, err := host.LoadManifest(fakeID); err == nil {
					m.Name = "b"
					_ = m.Save()
				}
			}()
		}
		return ok
	})
	if err := host.Rename(fakeID, "b"); err != nil {
		t.Fatalf("rename to b: %v", err)
	}
	// Delayed stale save restores "a" on disk, then socket disappears while
	// the legacy holder process (cmd.Process.Pid) is still alive.
	if err := stale.Save(); err != nil {
		t.Fatal(err)
	}
	_ = ln.Close()
	_ = os.Remove(paths.Socket(fakeID))

	for _, name := range []string{"a", "b"} {
		if res, err := host.DeleteNamed(fakeID, name); codeOf(err) != "session_renamed" || res.Deleted {
			t.Fatalf("DeleteNamed(%q) while legacy process still lives without socket: %+v, %v", name, res, err)
		}
	}
	if uName, uncertain := readUnsavedRecord(t); uName != "b" || uncertain {
		t.Fatalf("rename.unsaved while process lives = (%q, %v), want (\"b\", false)", uName, uncertain)
	}

	// Once the legacy process actually exits, reconciliation saves "b",
	// refuses "a", and allows "b".
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	reaped = true

	if res, err := host.DeleteNamed(fakeID, "a"); codeOf(err) != "session_renamed" || !strings.Contains(err.Error(), `now named "b"`) || res.Deleted {
		t.Fatalf("DeleteNamed(\"a\") after legacy process exited: %+v, %v", res, err)
	}
	if res, err := host.DeleteNamed(fakeID, "b"); err != nil || !res.Deleted {
		t.Fatalf("DeleteNamed(\"b\") after legacy process exited: %+v, %v", res, err)
	}
}
