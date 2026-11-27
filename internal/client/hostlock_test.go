package client_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
)

// ownState gives the test its own home and state folder, so the records of
// removals and the locks of hosts are the test's own, never the user's.
// USERPROFILE is the home on Windows.
func ownState(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
}

// ownState moves the home, as the client finds it, too: a lookup that
// falls back to the home, such as the state folder without
// XDG_STATE_HOME, stays in the test's folders.
func TestOwnStateMovesTheHome(t *testing.T) {
	before, _ := os.UserHomeDir()
	ownState(t)
	home, err := os.UserHomeDir()
	if err != nil || home == before || home != os.Getenv("HOME") {
		t.Fatalf("the home is %q (%v), before %q", home, err, before)
	}
	t.Setenv("XDG_STATE_HOME", "")
	if dir := client.StateDir(); !filepath.IsAbs(dir) || filepath.Dir(filepath.Dir(filepath.Dir(dir))) != home {
		t.Errorf("the state folder %q is not in the test's home %q", dir, home)
	}
}

// One command at a time holds the lock of a host, and the error names the
// kind of command that holds it. A command's own steps share its lock
// through the context. Box and box have two locks, and a name that cannot
// name a host has nothing to lock.
func TestLockHost(t *testing.T) {
	ownState(t)
	ctx, unlock, err := client.LockHost(context.Background(), "box", "host rm")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = client.LockHost(context.Background(), "box", "host create")
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "host_busy" || ue.Msg != "box is busy: host rm changes it" || ue.Hint == "" {
		t.Fatalf("a second lock: %+v", err)
	}
	_, inner, err := client.LockHost(ctx, "box", "host add")
	if err != nil {
		t.Fatalf("the command's own step: %v", err)
	}
	inner()
	if _, _, err := client.LockHost(context.Background(), "box", "host set"); err == nil {
		t.Fatal("the step's unlock let the command's lock go")
	}
	_, other, err := client.LockHost(context.Background(), "Box", "host rm")
	if err != nil {
		t.Fatalf("Box shares the lock of box: %v", err)
	}
	other()
	unlock()
	_, again, err := client.LockHost(context.Background(), "box", "host set")
	if err != nil {
		t.Fatalf("the lock after the unlock: %v", err)
	}
	again()
	_, none, err := client.LockHost(context.Background(), "../x", "host rm")
	if err != nil {
		t.Fatal(err)
	}
	none()
}

// A context that outlives its lock no longer holds it: after the unlock,
// a step with that context takes the lock again, and finds it busy while
// another command holds it.
func TestAContextAfterItsLockIsLetGo(t *testing.T) {
	ownState(t)
	ctx, unlock, err := client.LockHost(context.Background(), "box", "setup")
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	_, other, err := client.LockHost(context.Background(), "box", "host rm")
	if err != nil {
		t.Fatal(err)
	}
	defer other()
	_, _, err = client.LockHost(ctx, "box", "setup")
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "host_busy" {
		t.Fatalf("a step after the unlock, while host rm holds the lock: %+v", err)
	}
}

// A lock that cannot be made, as in a state folder where a file stands in
// the way, is a user error that names the file to move. It fails the same
// way on every system.
func TestALockThatCannotBeMade(t *testing.T) {
	ownState(t)
	if err := os.MkdirAll(client.StateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	hosts := filepath.Join(client.StateDir(), "hosts")
	if err := os.WriteFile(hosts, []byte("in the way"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := client.LockHost(context.Background(), "plain", "host rm")
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "lock_failed" || ue.Hint != "Move the file "+hosts+" out of the way, then run the command again." {
		t.Fatalf("err = %+v", err)
	}
}

// The record that ClearRemoval cannot delete is a user error that says
// what to delete by hand. A folder that is not empty, where the record
// should be, cannot be removed on any system.
func TestClearRemovalSaysWhatToDo(t *testing.T) {
	ownState(t)
	r := client.LoadRemoval(client.Host{Name: "box"})
	if err := r.Record(client.RemovalDestroy); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(client.StateDir(), "hosts", "box-*", "removal.json"))
	if len(files) != 1 {
		t.Fatalf("the record: %v", files)
	}
	if err := os.Remove(files[0]); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(files[0], "in"), 0o700); err != nil {
		t.Fatal(err)
	}
	err := client.ClearRemoval("box")
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "removal_record" || ue.Hint != "Delete "+files[0]+" by hand, then run the command again." {
		t.Fatalf("err = %+v", err)
	}
}
