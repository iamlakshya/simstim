package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// ownLockFiles gives the test its own hosts.toml and state folder, and a
// wait for the lock of hosts.toml that says so after note and gives up
// after limit.
func ownLockFiles(t *testing.T, note, limit time.Duration) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	oldNote, oldWait := hostsNote, hostsWait
	hostsNote, hostsWait = note, limit
	t.Cleanup(func() { hostsNote, hostsWait = oldNote, oldWait })
}

// noteLine is a writer of notes that a test waits for.
type noteLine struct {
	mu   sync.Mutex
	text string
	once sync.Once
	got  chan struct{}
}

func (n *noteLine) Write(p []byte) (int, error) {
	n.mu.Lock()
	n.text += string(p)
	n.mu.Unlock()
	n.once.Do(func() { close(n.got) })
	return len(p), nil
}

// A save that waits for the lock of hosts.toml says so after a moment,
// and stops at once when its context ends, as at Ctrl+C: it saves
// nothing, and lets the lock of its host go, with its .who file.
func TestAWaitForHostsTomlStopsAtACancel(t *testing.T) {
	ownLockFiles(t, 50*time.Millisecond, time.Minute)
	box := Host{Name: "box", SSH: "box"}
	if err := SaveHosts(context.Background(), []Host{box}); err != nil {
		t.Fatal(err)
	}
	held, err := lockHostsFile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held()
	notes := &noteLine{got: make(chan struct{})}
	ctx, cancel := context.WithCancel(WithNotes(context.Background(), notes))
	done := make(chan error, 1)
	go func() { done <- UpdateHosts(ctx, HostChange{Kind: "host rm", Was: &box}) }()
	<-notes.got
	if who := hostLockPath("box") + ".who"; !exists(who) {
		t.Error("the waiting forget does not hold the lock of its host")
	}
	cancel()
	err = <-done
	notes.mu.Lock()
	said := notes.text
	notes.mu.Unlock()
	if !errors.Is(err, context.Canceled) || said != "Waiting for another command that saves "+hostsPath()+"…\n" {
		t.Fatalf("the forget: %v, said %q", err, said)
	}
	if exists(hostLockPath("box") + ".who") {
		t.Error("the .who file stayed")
	}
	if _, err := FindHost("box"); err != nil {
		t.Error("the cancelled forget saved")
	}
}

// The wait has an end: after it, the save fails with hosts_busy, which
// says what to end, and saves nothing. Without notes, a wait says
// nothing.
func TestAWaitForHostsTomlEnds(t *testing.T) {
	ownLockFiles(t, time.Millisecond, 150*time.Millisecond)
	held, err := lockHostsFile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held()
	box := Host{Name: "box", SSH: "box"}
	err = UpdateHosts(context.Background(), HostChange{Kind: "host add", Now: &box})
	var ue *UserError
	if !errors.As(err, &ue) || ue.Code != "hosts_busy" || !strings.Contains(ue.Msg, "held "+hostsPath()+" for 150ms") ||
		!strings.HasPrefix(ue.Hint, "End that command") || ue.Kind != KindFailed {
		t.Fatalf("err = %+v", err)
	}
	if hosts, _ := LoadHosts(); len(hosts) != 0 {
		t.Errorf("the hosts are %+v", hosts)
	}
}

// A lock that cannot be made names the nearest folder that exists, or
// the file that stands in the path. An error of the lock itself, such as
// on a network disk without file locks, asks for a local disk.
func TestTheHintsOfALockThatFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state", "hosts", "box.lock")
	ue := asUserError(t, lockFailed("the host box", path, os.ErrPermission))
	if ue.Hint != "Make "+dir+" writable, then run the command again." {
		t.Errorf("a path whose folders are missing: %q", ue.Hint)
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ue = asUserError(t, lockFailed("the host box", filepath.Join(file, "hosts", "box.lock"), os.ErrExist))
	if ue.Hint != "Move the file "+file+" out of the way, then run the command again." {
		t.Errorf("a file in the path: %q", ue.Hint)
	}
	ue = asUserError(t, flockFailed("hosts.toml", path, syscall.ENOLCK))
	if ue.Code != "lock_failed" || !strings.Contains(ue.Hint, "on a local disk") || strings.Contains(ue.Hint, "writable") {
		t.Errorf("a lock that the system refused: %+v", ue)
	}
}

// A lock that the system refuses, the lock of a host or of hosts.toml, is
// lock_failed with the hint for a local disk.
func TestALockThatTheSystemRefuses(t *testing.T) {
	ownLockFiles(t, time.Second, time.Minute)
	old := takeLock
	takeLock = func(*os.File) error { return syscall.ENOLCK }
	t.Cleanup(func() { takeLock = old })
	_, _, err := LockHost(context.Background(), "box", "host rm")
	if ue := asUserError(t, err); ue.Code != "lock_failed" || !strings.Contains(ue.Hint, "on a local disk") {
		t.Errorf("the lock of a host: %+v", ue)
	}
	_, err = lockHostsFile(context.Background())
	if ue := asUserError(t, err); ue.Code != "lock_failed" || !strings.Contains(ue.Hint, "on a local disk") {
		t.Errorf("the lock of hosts.toml: %+v", ue)
	}
}

// A cancelled update never saves: not with a context that ended before it
// began, and not with a cancel just before its save.
func TestACancelledUpdateNeverSaves(t *testing.T) {
	ownLockFiles(t, time.Second, time.Minute)
	box := Host{Name: "box", SSH: "box"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := UpdateHosts(ctx, HostChange{Kind: "host add", Now: &box}); !errors.Is(err, context.Canceled) {
		t.Errorf("an update with a context that had ended: %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	old := beforeHostsSave
	beforeHostsSave = cancel
	t.Cleanup(func() { beforeHostsSave = old })
	if err := UpdateHosts(ctx, HostChange{Kind: "host add", Now: &box}); !errors.Is(err, context.Canceled) {
		t.Errorf("an update cancelled just before its save: %v", err)
	}
	if hosts, _ := LoadHosts(); len(hosts) != 0 {
		t.Errorf("a cancelled update saved %+v", hosts)
	}
}

// A cancel that comes as a waiting update takes the lock of hosts.toml
// gives no lock: the lock is let go at once.
func TestACancelAsTheLockIsTaken(t *testing.T) {
	ownLockFiles(t, time.Second, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	old := takeLock
	takeLock = func(f *os.File) error {
		err := old(f)
		if err == nil {
			cancel()
		}
		return err
	}
	t.Cleanup(func() { takeLock = old })
	if _, err := lockHostsFile(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("the lock as the cancel came: %v", err)
	}
	takeLock = old
	unlock, err := lockHostsFile(context.Background())
	if err != nil {
		t.Fatalf("the lock stayed taken: %v", err)
	}
	unlock()
}

// A short wait for the lock of hosts.toml says nothing: the note comes
// only after hostsNote.
func TestAShortWaitSaysNothing(t *testing.T) {
	ownLockFiles(t, time.Second, time.Minute)
	held, err := lockHostsFile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	waiting := make(chan struct{})
	var once sync.Once
	old := takeLock
	takeLock = func(f *os.File) error {
		err := old(f)
		if errors.Is(err, errLockTaken) {
			once.Do(func() { close(waiting) })
		}
		return err
	}
	t.Cleanup(func() { takeLock = old })
	notes := &noteLine{got: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		unlock, err := lockHostsFile(WithNotes(context.Background(), notes))
		if err == nil {
			unlock()
		}
		done <- err
	}()
	<-waiting
	held()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	notes.mu.Lock()
	defer notes.mu.Unlock()
	if notes.text != "" {
		t.Errorf("a short wait said %q", notes.text)
	}
}

func asUserError(t *testing.T, err error) *UserError {
	t.Helper()
	var ue *UserError
	if !errors.As(err, &ue) {
		t.Fatalf("not a user error: %v", err)
	}
	return ue
}
