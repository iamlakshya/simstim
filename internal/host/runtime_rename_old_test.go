package host_test

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
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
	"github.com/iamlakshya/simstim/internal/version"
)

// A rename through the holder of an older helper (verify 8, finding 1).
// Such a holder answers a rename before it saves the name, at its next
// tick, and never takes the session's lock.

// oldHolder serves the session fakeID as an older holder: it answers a
// rename at once, and saves the name only when save closes. Its later
// requests, such as a delete's kill, end it.
func oldHolder(t *testing.T, took chan<- struct{}, save <-chan struct{}) {
	t.Helper()
	serveHolder(t, nil, func(n int) (*proto.Response, bool) {
		if n > 1 {
			return ok, true
		}
		go func() {
			<-save
			m, err := host.LoadManifest(fakeID)
			if err == nil {
				m.Name = "b"
				err = m.Save()
			}
			if err != nil {
				t.Errorf("the old holder's save: %v", err)
			}
		}()
		close(took)
		return ok, false
	})
}

// The rename holds the session's lock until the old holder's save: a
// delete that the user confirmed under the old name, after the holder
// answered the rename, never deletes the renamed session.
func TestAnOldHoldersRenameHoldsTheLockUntilItsSave(t *testing.T) {
	hosttest.New(t)
	stoppedSession(t)
	took, save := make(chan struct{}), make(chan struct{})
	oldHolder(t, took, save)
	renamed := make(chan error, 1)
	go func() { renamed <- host.Rename(fakeID, "b") }()
	<-took
	for range 20 {
		if res, err := host.DeleteNamed(fakeID, "a"); err == nil || res.Deleted {
			t.Fatalf("a delete under the old name, before the holder's save: %+v, %v", res, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	close(save)
	if err := <-renamed; err != nil {
		t.Fatalf("the rename: %v", err)
	}
	if _, err := host.DeleteNamed(fakeID, "a"); codeOf(err) != "session_renamed" || !strings.Contains(err.Error(), `now named "b"`) {
		t.Errorf("a delete under the old name, after the save: %v", err)
	}
}

// An old holder that does not save the name in time: the rename says so,
// and records the name. A guarded delete then fails closed under either
// name, until the manifest has the new name.
func TestAnOldHoldersUnsavedRenameStopsADelete(t *testing.T) {
	hosttest.New(t)
	stoppedSession(t)
	old := *host.RenameSaveWait
	t.Cleanup(func() { *host.RenameSaveWait = old })
	*host.RenameSaveWait = 200 * time.Millisecond
	took, save := make(chan struct{}), make(chan struct{})
	oldHolder(t, took, save)
	err := host.Rename(fakeID, "b")
	if codeOf(err) != "busy" || !strings.Contains(err.Error(), "did not save it in time") || strings.Count(hintOf(err), ".") != 1 {
		t.Fatalf("the rename: %v (hint %q)", err, hintOf(err))
	}
	for _, name := range []string{"a", "b"} {
		res, err := host.DeleteNamed(fakeID, name)
		if codeOf(err) != "session_renamed" || !strings.Contains(err.Error(), "not saved yet") || res.Deleted {
			t.Errorf("a delete under %s, before the holder's save: %+v, %v", name, res, err)
		}
	}
	close(save)
	hosttest.WaitFor(t, 5*time.Second, "the old holder's save", func() bool {
		m, err := host.LoadManifest(fakeID)
		return err == nil && m.Name == "b"
	})
	if _, err := host.DeleteNamed(fakeID, "a"); codeOf(err) != "session_renamed" || !strings.Contains(err.Error(), `now named "b"`) {
		t.Errorf("a delete under the old name, after the save: %v", err)
	}
	if res, err := host.DeleteNamed(fakeID, "b"); err != nil || !res.Deleted {
		t.Errorf("a delete under the new name, after the save: %+v, %v", res, err)
	}
}

// A rename from a to b that was not saved, followed by a rename back to a,
// does not count the stale manifest (still "a" from before b) as the old
// holder's save. And an old holder that stalls before answering its hello
// still keeps a guarded delete closed.
func TestAnOldHoldersUnsavedRenameWaitsPastAStaleManifestAndStalledHello(t *testing.T) {
	hosttest.New(t)
	stoppedSession(t)
	old := *host.RenameSaveWait
	t.Cleanup(func() { *host.RenameSaveWait = old })
	*host.RenameSaveWait = 150 * time.Millisecond
	saveA := make(chan struct{})
	serveHolder(t, nil, func(n int) (*proto.Response, bool) {
		if n == 2 {
			go func() {
				<-saveA
				if m, err := host.LoadManifest(fakeID); err == nil {
					m.Name = "a"
					_ = m.Save()
				}
			}()
		}
		return ok, n > 2
	})
	if err := host.Rename(fakeID, "b"); codeOf(err) != "busy" {
		t.Fatalf("rename to b: %v", err)
	}
	if err := host.Rename(fakeID, "a"); codeOf(err) != "busy" {
		t.Fatalf("rename back to a before any save succeeded against stale manifest: %v", err)
	}
	if res, err := host.DeleteNamed(fakeID, "a"); codeOf(err) != "session_renamed" || res.Deleted {
		t.Fatalf("delete under a before the holder saved a: %+v, %v", res, err)
	}
	close(saveA)
	hosttest.WaitFor(t, 5*time.Second, "the old holder's save of a", func() bool {
		res, err := host.DeleteNamed(fakeID, "b")
		return codeOf(err) == "session_renamed" && strings.Contains(err.Error(), `now named "a"`) && !res.Deleted
	})

	// A holder whose socket accepts but stalls before sending its hello
	// counts as an older holder, so an unsaved rename still blocks delete.
	t.Run("stalled hello", func(t *testing.T) {
		hosttest.New(t)
		stoppedSession(t)
		serveHolder(t, nil, func(int) (*proto.Response, bool) { return ok, true })
		if err := host.Rename(fakeID, "y"); codeOf(err) != "busy" {
			t.Fatalf("seed unsaved rename: %v", err)
		}
		done := make(chan struct{})
		defer close(done)
		ln, err := net.Listen("unix", paths.Socket(fakeID))
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				go func() {
					defer c.Close()
					<-done
				}()
			}
		}()
		if res, err := host.DeleteNamed(fakeID, "a"); codeOf(err) != "session_renamed" || res.Deleted {
			t.Errorf("a delete while the old holder stalls its hello: %+v, %v", res, err)
		}
	})
}

// New skips any candidate ID that an existing session already uses as its
// ID or, from an older helper, as its name (decision 9 note).
func TestNewSessionIDSkipsExistingIDsAndNames(t *testing.T) {
	hosttest.New(t)
	if err := (&host.Manifest{Schema: 1, ID: "efgh2345", Name: "abcd2345"}).Save(); err != nil {
		t.Fatal(err)
	}
	oldDraw := *host.DrawID
	t.Cleanup(func() { *host.DrawID = oldDraw })
	draws := []string{"abcd2345", "efgh2345", "zzzz2345"}
	var idx atomic.Int32
	*host.DrawID = func() string {
		i := int(idx.Add(1)) - 1
		if i < len(draws) {
			return draws[i]
		}
		return oldDraw()
	}
	s, err := host.New(host.NewArgs{Name: "fresh", Cwd: "~", Argv: []string{"sleep", "60"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if s.ID != "zzzz2345" {
		t.Fatalf("new chose ID %q, want zzzz2345 after skipping existing name and ID", s.ID)
	}
	if m, err := host.LoadManifest("efgh2345"); err != nil || m.Name != "abcd2345" {
		t.Errorf("existing session after new: %+v, %v", m, err)
	}
}

// readUnsavedRecord reads rename.unsaved from the test session's folder.
func readUnsavedRecord(t *testing.T) (name string, uncertain bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(paths.SessionDir(fakeID), "rename.unsaved"))
	if err != nil {
		t.Fatalf("read rename.unsaved: %v", err)
	}
	var u struct {
		Name      string `json:"name"`
		Uncertain bool   `json:"uncertain"`
	}
	if err := json.Unmarshal(b, &u); err != nil {
		t.Fatalf("unmarshal rename.unsaved: %v", err)
	}
	return u.Name, u.Uncertain
}

// serveClosableLegacyHolder serves fakeID as an older holder (without
// renameSaves) and returns its listener so tests can simulate a crash or
// unlinked socket at a chosen moment.
func serveClosableLegacyHolder(t *testing.T, answer func(n int, req proto.Request) *proto.Response) net.Listener {
	t.Helper()
	sock := paths.Socket(fakeID)
	if err := paths.EnsurePrivateDir(filepath.Dir(sock)); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var n atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				r, w := bufio.NewReader(c), proto.NewWriter(c)
				if f, err := proto.Read(r); err != nil || f.Type != proto.THello {
					return
				}
				_ = w.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "holder"})
				f, err := proto.Read(r)
				if err != nil {
					return
				}
				var req proto.Request
				_ = json.Unmarshal(f.Payload, &req)
				if resp := answer(int(n.Add(1)), req); resp != nil {
					resp.ID = req.ID
					_ = w.WriteJSON(proto.TResponse, resp)
				}
			}()
		}
	}()
	return ln
}

// When an older holder accepts a rename, saves the new name to manifest.json,
// and either drops the reply or does not answer in time, the pre-written
// uncertain rename record stays on disk and guarded deletes under BOTH the
// old and new name fail closed (decision 10 item 1, decision 11 item 7).
func TestLegacyRenameUncertainReplyFailsClosed(t *testing.T) {
	t.Run("dropped reply", func(t *testing.T) {
		hosttest.New(t)
		stoppedSession(t)
		var accepted atomic.Int32
		serveHolder(t, nil, func(int) (*proto.Response, bool) {
			accepted.Add(1)
			if m, err := host.LoadManifest(fakeID); err == nil {
				m.Name = "b"
				_ = m.Save()
			}
			return nil, false
		})
		if err := host.Rename(fakeID, "b"); codeOf(err) != "busy" {
			t.Fatalf("rename with dropped reply: %v", err)
		}
		if accepted.Load() == 0 {
			t.Fatal("legacy holder never received rename")
		}
		if m, err := host.LoadManifest(fakeID); err != nil || m.Name != "b" {
			t.Fatalf("manifest after holder save on dropped reply: %+v, %v", m, err)
		}
		if uName, uncertain := readUnsavedRecord(t); uName != "b" || !uncertain {
			t.Fatalf("rename.unsaved = (%q, uncertain=%v), want (\"b\", true)", uName, uncertain)
		}
		for _, name := range []string{"a", "b"} {
			if res, err := host.DeleteNamed(fakeID, name); codeOf(err) != "session_renamed" || res.Deleted {
				t.Errorf("DeleteNamed(%q) after dropped reply: %+v, %v", name, res, err)
			}
		}
	})

	t.Run("timed out reply", func(t *testing.T) {
		hosttest.New(t)
		stoppedSession(t)
		oldWait := *host.RenameWait
		t.Cleanup(func() { *host.RenameWait = oldWait })
		// renameRunning gives holderRequest renameWait + 5 s; -4850 ms makes
		// the request deadline 150 ms while uncontended locks still succeed.
		*host.RenameWait = -4850 * time.Millisecond
		hang := make(chan struct{})
		t.Cleanup(func() { close(hang) })
		saved := make(chan struct{})
		serveHolder(t, nil, func(n int) (*proto.Response, bool) {
			if n == 1 {
				go func() {
					if m, err := host.LoadManifest(fakeID); err == nil {
						m.Name = "b"
						_ = m.Save()
					}
					close(saved)
				}()
			}
			<-hang
			return nil, false
		})
		if err := host.Rename(fakeID, "b"); codeOf(err) != "busy" {
			t.Fatalf("rename with timed out reply: %v", err)
		}
		<-saved
		*host.RenameWait = oldWait
		if m, err := host.LoadManifest(fakeID); err != nil || m.Name != "b" {
			t.Fatalf("manifest after holder save on timed out reply: %+v, %v", m, err)
		}
		if uName, uncertain := readUnsavedRecord(t); uName != "b" || !uncertain {
			t.Fatalf("rename.unsaved = (%q, uncertain=%v), want (\"b\", true)", uName, uncertain)
		}
		for _, name := range []string{"a", "b"} {
			if res, err := host.DeleteNamed(fakeID, name); codeOf(err) != "session_renamed" || res.Deleted {
				t.Errorf("DeleteNamed(%q) after timed out reply: %+v, %v", name, res, err)
			}
		}
	})
}

// When recording the legacy-holder rename intent fails after reading existing
// state, Rename stops before sending the rename over the socket, leaving the
// older holder untouched. And an explicit non-exiting rejection from the
// older holder clears or restores the intent record cleanly (decision 11 item 7).
func TestLegacyRenameIntentWriteFailureStopsBeforeSend(t *testing.T) {
	hosttest.New(t)
	stoppedSession(t)
	var received atomic.Int32
	serveHolder(t, nil, func(n int) (*proto.Response, bool) {
		received.Add(1)
		if n > 1 {
			return ok, true
		}
		return &proto.Response{Error: &proto.Error{Code: "bad_name", Message: "rejected"}}, false
	})
	oldWrite := *host.WriteUnsavedFile
	t.Cleanup(func() { *host.WriteUnsavedFile = oldWrite })
	*host.WriteUnsavedFile = func(string, []byte) error {
		return errors.New("injected write failure")
	}
	if err := host.Rename(fakeID, "b"); codeOf(err) != "save" || hintOf(err) == "" {
		t.Fatalf("rename with failing intent write: %v (hint %q)", err, hintOf(err))
	}
	if n := received.Load(); n != 0 {
		t.Fatalf("legacy holder received %d requests despite failed intent write", n)
	}

	*host.WriteUnsavedFile = oldWrite
	if err := host.Rename(fakeID, "b"); codeOf(err) != "bad_name" {
		t.Fatalf("rename explicitly rejected by holder: %v", err)
	}
	if received.Load() != 1 {
		t.Fatalf("legacy holder received %d requests, want 1", received.Load())
	}
	if res, err := host.DeleteNamed(fakeID, "a"); err != nil || !res.Deleted {
		t.Errorf("DeleteNamed(\"a\") after explicit rejection: %+v, %v", res, err)
	}
}

// If the helper is interrupted right after the older holder answers the
// rename, before confirmRename runs, the intent record written before the
// request already blocks guarded deletes (decision 10, item 1).
func TestLegacyRenameInterruptedBeforeConfirmBlocksDelete(t *testing.T) {
	hosttest.New(t)
	stoppedSession(t)
	oldHook := *host.BeforeConfirmRename
	t.Cleanup(func() { *host.BeforeConfirmRename = oldHook })
	*host.BeforeConfirmRename = func(string) error { return errors.New("interrupted before confirm") }
	serveHolder(t, nil, func(int) (*proto.Response, bool) { return ok, false })
	if err := host.Rename(fakeID, "b"); err == nil || !strings.Contains(err.Error(), "interrupted before confirm") {
		t.Fatalf("interrupted rename: %v", err)
	}
	for _, name := range []string{"a", "b"} {
		if res, err := host.DeleteNamed(fakeID, name); codeOf(err) != "session_renamed" || res.Deleted {
			t.Errorf("DeleteNamed(%q) after interruption before confirm: %+v, %v", name, res, err)
		}
	}
}

// Even after an older holder saves the new name so confirmRename succeeds,
// the authoritative rename record stays on disk for the lifetime of that
// older holder: if a delayed earlier copy writes the stale old name back to
// manifest.json, DeleteNamed still refuses under either name, and New still
// reserves the older holder's authoritative name (decision 10, item 2).
func TestLegacyRenameDelayedStaleSaveStillBlocksDelete(t *testing.T) {
	hosttest.New(t)
	stoppedSession(t)
	stale, err := host.LoadManifest(fakeID)
	if err != nil {
		t.Fatal(err)
	}
	const target = "oldoldid"
	serveHolder(t, nil, func(n int) (*proto.Response, bool) {
		if n == 1 {
			go func() {
				if m, err := host.LoadManifest(fakeID); err == nil {
					m.Name = target
					_ = m.Save()
				}
			}()
		}
		return ok, n > 1
	})
	if err := host.Rename(fakeID, target); err != nil {
		t.Fatalf("rename to %s: %v", target, err)
	}
	// Simulate an older holder's delayed save of a snapshot copied before
	// the rename, overwriting manifest.json back to "a".
	if err := stale.Save(); err != nil {
		t.Fatal(err)
	}
	if m, err := host.LoadManifest(fakeID); err != nil || m.Name != "a" {
		t.Fatalf("manifest after delayed stale save: %+v, %v", m, err)
	}
	for _, name := range []string{"a", target} {
		if res, err := host.DeleteNamed(fakeID, name); codeOf(err) != "session_renamed" || res.Deleted {
			t.Errorf("DeleteNamed(%q) after delayed stale save: %+v, %v", name, res, err)
		}
	}
	oldDraw := *host.DrawID
	t.Cleanup(func() { *host.DrawID = oldDraw })
	var idx atomic.Int32
	*host.DrawID = func() string {
		if idx.Add(1) == 1 {
			return target
		}
		return "zzzz2345"
	}
	s, err := host.New(host.NewArgs{Name: "fresh", Cwd: "~", Argv: []string{"sleep", "60"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("new after delayed stale save: %v", err)
	}
	if s.ID != "zzzz2345" {
		t.Errorf("new chose %q instead of skipping legacy holder's authoritative name %q", s.ID, target)
	}
}
