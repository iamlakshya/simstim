package host_test

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// A delete bound to a name, and a rename, of the same session
// (docs/contributing/cli-spec.md 9.5). They take the same lock, and a
// rename is in the manifest before it answers, so the delete reads the
// name there alone.

// codeOf is the code of a host error, or "".
func codeOf(err error) string {
	var pe *proto.Error
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

// A rename that comes after the delete's check of the name, and before the
// delete, waits for the lock: it never renames a session that the delete
// then removes under the old name. The holder renames under the lock, so
// it waits until the delete has stopped it, and the rename fails.
func TestRenameWaitsForAGuardedDelete(t *testing.T) {
	hosttest.New(t)
	s := start(t, "a", "sleep", "60")
	oldHook := *host.AfterNameCheck
	t.Cleanup(func() { *host.AfterNameCheck = oldHook })
	renamed := make(chan error, 1)
	var nameThen string
	answered := false
	*host.AfterNameCheck = func(id string) {
		go func() { renamed <- host.Rename(id, "b") }()
		select {
		case err := <-renamed:
			answered = true
			renamed <- err
		case <-time.After(500 * time.Millisecond):
		}
		if m, err := host.LoadManifest(id); err == nil {
			nameThen = m.Name
		}
	}
	res, err := host.DeleteNamed(s.ID, "a")
	if err != nil || !res.Deleted {
		t.Fatalf("the guarded delete: %+v, %v", res, err)
	}
	if answered || nameThen != "a" {
		t.Errorf("a rename inside the delete's lock answered at once, and the name became %q", nameThen)
	}
	select {
	case err := <-renamed:
		// The holder went without an answer: the rename finds no session,
		// and never fails with the closed connection.
		if codeOf(err) != "not_found" {
			t.Errorf("the rename of a deleted session: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the rename never ended")
	}
	if _, err := os.Stat(paths.SessionDir(s.ID)); !os.IsNotExist(err) {
		t.Errorf("the session's folder is still there: %v", err)
	}
}

// A rename that the holder acknowledged is in the manifest: a delete that
// expects the old name refuses, also when the holder then answers nothing.
func TestAnAcknowledgedRenameStopsAnOldDelete(t *testing.T) {
	hosttest.New(t)
	s := start(t, "a", "sleep", "60")
	if err := host.Rename(s.ID, "b"); err != nil {
		t.Fatal(err)
	}
	pid := hosttest.HolderPID(t, s.ID)
	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGCONT) })
	if _, err := host.DeleteNamed(s.ID, "a"); codeOf(err) != "session_renamed" {
		t.Errorf("a delete that expects the old name: %v", err)
	}
	if _, err := os.Stat(paths.SessionDir(s.ID)); err != nil {
		t.Errorf("the session went: %v", err)
	}
}

// A guarded delete fails closed when the manifest cannot be read. An
// unguarded one, as the helper's own rm, still deletes.
func TestAGuardedDeleteFailsClosed(t *testing.T) {
	hosttest.New(t)
	s := start(t, "a", "sleep", "60")
	pid := hosttest.HolderPID(t, s.ID)
	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGCONT) })
	if err := os.WriteFile(filepath.Join(paths.SessionDir(s.ID), "manifest.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := host.DeleteNamed(s.ID, "a"); codeOf(err) != "name_unknown" {
		t.Errorf("a guarded delete of a manifest that does not parse: %v", err)
	}
	_ = syscall.Kill(pid, syscall.SIGCONT)
	if res, err := host.Delete(s.ID); err != nil || !res.Deleted {
		t.Errorf("an unguarded delete: %+v, %v", res, err)
	}
}

// A delete bound to the empty name of a session with no name refuses once
// the session has a name. A delete that is not bound to a name still
// deletes it.
func TestAGuardedDeleteOfASessionWithNoName(t *testing.T) {
	hosttest.New(t)
	s := start(t, "a", "sleep", "60")
	hosttest.OldName(t, s.ID, "")
	if err := host.Rename(s.ID, "b"); err != nil {
		t.Fatal(err)
	}
	if _, err := host.DeleteNamed(s.ID, ""); codeOf(err) != "session_renamed" {
		t.Errorf("a delete bound to no name, of a session named b: %v", err)
	}
	if res, err := host.Delete(s.ID); err != nil || !res.Deleted {
		t.Errorf("a delete bound to no name at all: %+v, %v", res, err)
	}
}

// The helper's own delete takes an ID alone: a session named like an ID
// that does not exist is not deleted (the e2e clean-up relies on this).
// Such a name is refused now, and an older helper could give it.
func TestTheHelpersDeleteTakesAnIDAlone(t *testing.T) {
	hosttest.New(t)
	s := start(t, "a", "sleep", "60")
	hosttest.OldName(t, s.ID, "abcdefgh")
	if _, err := host.Delete("abcdefgh"); codeOf(err) != "not_found" {
		t.Errorf("a delete of an ID that does not exist: %v", err)
	}
	if _, err := os.Stat(paths.SessionDir(s.ID)); err != nil {
		t.Errorf("the session named like that ID went: %v", err)
	}
}

// A rename that the holder got, and that stalls before it takes the lock,
// goes on only after the delete that checked the old name is done: the
// delete never removes a session that a rename renamed (V2). Here the
// stall ends right after the delete's check of the name.
func TestARenameStalledInTheHolderWaitsForTheDelete(t *testing.T) {
	hosttest.New(t)
	s := start(t, "a", "sleep", "60")
	stall := filepath.Join(paths.SessionDir(s.ID), host.RenameStall)
	if err := os.WriteFile(stall, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	renamed := make(chan error, 1)
	go func() { renamed <- host.Rename(s.ID, "b") }()
	hosttest.WaitFor(t, 10*time.Second, "the rename to stall in the holder", func() bool {
		_, err := os.Stat(filepath.Join(paths.SessionDir(s.ID), host.RenameStalled))
		return err == nil
	})
	oldHook := *host.AfterNameCheck
	t.Cleanup(func() { *host.AfterNameCheck = oldHook })
	var during error
	answered := false
	*host.AfterNameCheck = func(string) {
		_ = os.Remove(stall)
		select {
		case during = <-renamed:
			answered = true
		case <-time.After(time.Second):
		}
	}
	res, err := host.DeleteNamed(s.ID, "a")
	if answered && during == nil && res.Deleted {
		t.Fatal("the delete removed a session that a rename had renamed")
	}
	if err != nil || !res.Deleted {
		t.Errorf("the delete of a: %+v, %v", res, err)
	}
	if !answered {
		select {
		case during = <-renamed:
		case <-time.After(30 * time.Second):
			t.Fatal("the rename never ended")
		}
	}
	if codeOf(during) != "not_found" {
		t.Errorf("the rename of a deleted session: %v", during)
	}
}

// A rename between a new holder's load of the manifest and its listen
// waits for the start's lock, so the holder's first save does not put the
// old name back (S5).
func TestARenameDuringAStartIsNotLost(t *testing.T) {
	hosttest.New(t)
	stall := filepath.Join(t.TempDir(), "start-stall")
	if err := os.WriteFile(stall, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(host.StartStallEnv, stall)
	started := make(chan error, 1)
	go func() {
		_, err := host.New(host.NewArgs{Name: "a", Cwd: "~", Argv: []string{"sleep", "60"}, Cols: 80, Rows: 24})
		started <- err
	}()
	var id string
	hosttest.WaitFor(t, 10*time.Second, "the new holder to stall", func() bool {
		b, err := os.ReadFile(stall + ".stalled")
		id = string(b)
		return err == nil && id != ""
	})
	renamed := make(chan error, 1)
	go func() { renamed <- host.Rename(id, "b") }()
	// A rename that did not wait for the start's lock would be done by now.
	time.Sleep(300 * time.Millisecond)
	if err := os.Remove(stall); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		what string
		ch   chan error
	}{{"the start", started}, {"the rename", renamed}} {
		select {
		case err := <-c.ch:
			if err != nil {
				t.Fatalf("%s: %v", c.what, err)
			}
		case <-time.After(30 * time.Second):
			t.Fatalf("%s never ended", c.what)
		}
	}
	// The holder has the new name in memory too: its last save, at a
	// kill, keeps it. A rename that wrote only the manifest, while the
	// holder ran with the old name, would be lost here.
	pid := hosttest.HolderPID(t, id)
	_, _ = host.HolderRequest(id, "kill", nil, 3*time.Second)
	if err := hosttest.Reap(pid, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if m, err := host.LoadManifest(id); err != nil || m.Name != "b" {
		t.Errorf("a rename during the start was lost: %+v, %v", m, err)
	}
}

// A rename waits for the session's lock, which a resume, a restore or a
// delete holds, and then renames (S6): that of a running session, which
// its holder does, and that of a session that does not run.
func TestARenameWaitsForTheLock(t *testing.T) {
	hosttest.New(t)
	running := start(t, "a", "sleep", "60")
	stopped := start(t, "c", "sleep", "60")
	pid := hosttest.HolderPID(t, stopped.ID)
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if err := hosttest.Reap(pid, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	for id, name := range map[string]string{running.ID: "b", stopped.ID: "d"} {
		unlock, err := host.LockSession(id)
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			time.Sleep(300 * time.Millisecond)
			unlock()
		}()
		if err := host.Rename(id, name); err != nil {
			t.Errorf("a rename while the lock was held for 300 ms: %v", err)
		}
		if m, err := host.LoadManifest(id); err != nil || m.Name != name {
			t.Errorf("after the rename to %s: %+v, %v", name, m, err)
		}
	}
}
