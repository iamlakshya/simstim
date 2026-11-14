package supervisor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
)

// endBound is how long a supervisor may take to end its sandbox, with
// hostileDriver's shorter grace.
const endBound = 30 * time.Second

// endedWithin waits up to endBound for the supervisor's end.
func endedWithin(t *testing.T, ended <-chan error) error {
	t.Helper()
	select {
	case err := <-ended:
		return err
	case <-time.After(endBound):
		t.Fatalf("the supervisor did not end within %s", endBound)
	}
	return nil
}

// closed fails the test unless the session's record is closed and its
// socket has gone.
func closedRecord(t *testing.T, id, kind string) {
	t.Helper()
	m, err := host.LoadManifest(id)
	if err != nil || m.Life == nil || m.Life.End == nil || m.Life.End.Kind != kind {
		t.Errorf("the record = %+v, %v; want its end, %s", m.Life, err, kind)
	}
	if _, err := os.Stat(paths.Socket(id)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the session's socket is still there: %v", err)
	}
}

// A guest init that tries to leave its group, and stays alive after its
// link's end and its own end, never holds up the cleanup: the driver kills
// it by its PID too, and the supervisor ends within its bound. It cannot
// leave its group, which it leads in a session of its own. The test drops
// the driver's SIGKILL to the group, so only the kill by its PID ends it.
func TestAGuestInitThatStaysEndsWithTheSandbox(t *testing.T) {
	// noGroupKill lets the driver's SIGTERM to the group pass, and drops
	// its SIGKILL.
	noGroupKill := func(t *testing.T) {
		old := signalGroup
		signalGroup = func(pid int, sig syscall.Signal) error {
			if sig == syscall.SIGKILL {
				return nil
			}
			return old(pid, sig)
		}
		t.Cleanup(func() { signalGroup = old })
	}
	// leftNot fails the test unless the guest init's try to leave its
	// group failed.
	leftNot := func(t *testing.T, f string) {
		if b, err := os.ReadFile(f); err != nil || string(b) == "<nil>" {
			t.Errorf("the guest init's try to leave its group: %q, %v; want an error", b, err)
		}
	}
	t.Run("after a breach", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "setpgid")
		hostileDriver(t, initStays, f)
		noGroupKill(t)
		id, _ := plainSession(t)
		s, p, ended := startPlain(t, id)
		t.Cleanup(func() { _ = p.cmd.Process.Kill(); _ = p.cmd.Wait() })
		s.breach(errors.New("a test's breach"))
		_ = endedWithin(t, ended)
		reaped(t, p.cmd.Process.Pid)
		closedRecord(t, id, host.EndSandbox)
		leftNot(t, f)
	})
	t.Run("a boot past its deadline", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "setpgid")
		hostileDriver(t, initStays, f)
		noGroupKill(t)
		old := bootTimeout
		bootTimeout = 2 * time.Second
		t.Cleanup(func() { bootTimeout = old })
		id, _ := plainSession(t)
		sent, drivers := gate(t, false)
		ended := make(chan error, 1)
		go func() { ended <- Run(id) }()
		<-sent
		if err := endedWithin(t, ended); err == nil {
			t.Error("a boot past its deadline worked")
		}
		p := <-drivers
		t.Cleanup(func() { _ = p.cmd.Process.Kill(); _ = p.cmd.Wait() })
		reaped(t, p.cmd.Process.Pid)
		closedRecord(t, id, host.EndBoot)
		leftNot(t, f)
	})
}

// A guest init that crashes, and so never runs its end, still leaves its
// holder the time to end the agent's group: the driver gives the group
// groupGrace after SIGTERM, as after a signal. The agent and its child,
// which ignore SIGHUP and SIGTERM, then end with the holder's SIGKILL.
func TestACrashedGuestInitGetsTheGroupGrace(t *testing.T) {
	hostileDriver(t, initCrash, "3")
	id, pids := plainSession(t)
	_, p, ended := startPlain(t, id)
	agent, child := pids()
	_ = endedWithin(t, ended)
	if p.clean.Load() {
		t.Error("a guest init that crashed counts as a clean end")
	}
	goneWithin(t, 5*time.Second, agent, child)
	reaped(t, p.cmd.Process.Pid)
}

// An idle guest init ends cleanly on the driver's SIGTERM: its handler runs
// its end, and it exits with code 0. So Kill gives no group grace.
func TestAnIdleGuestInitEndsCleanly(t *testing.T) {
	testFolders(t)
	if err := paths.EnsurePrivateDir(paths.SessionDir("idle2345")); err != nil {
		t.Fatal(err)
	}
	sb, err := newSandbox(&host.Manifest{ID: "idle2345", Wall: &host.WallRecord{Driver: driverPlain}})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := sb.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	p := sb.(*plain)
	time.Sleep(300 * time.Millisecond) // for the guest init's handler
	start := time.Now()
	_ = sb.Kill()
	if took := time.Since(start); !p.clean.Load() || took >= groupGrace {
		t.Errorf("Kill of an idle guest init took %s, clean=%v; want a clean end at once", took, p.clean.Load())
	}
	reaped(t, p.cmd.Process.Pid)
}

// The driver's first signal to the guest init's group is SIGTERM: each
// process there gets to end what it started, before the last SIGKILL.
func TestTheGuestInitsGroupGetsSIGTERMFirst(t *testing.T) {
	id, _ := plainSession(t)
	marker := filepath.Join(t.TempDir(), "term")
	hostileDriver(t, initTermChild, marker)
	s, _, ended := startPlain(t, id)
	waitFor(t, "the child", func() bool {
		_, err := os.Stat(marker + ".ready")
		return err == nil
	})
	s.breach(errors.New("a test's breach"))
	_ = endedWithin(t, ended)
	if b, err := os.ReadFile(marker); err != nil || strings.TrimSpace(string(b)) != "TERM" {
		t.Errorf("a process in the guest init's group got no SIGTERM: %q, %v", b, err)
	}
}

// A process in the guest init's group that ignores SIGHUP and SIGTERM ends
// with the sandbox: the driver's last signal goes to the whole group. The
// process is no child of the guest init, whose end would kill it too.
func TestAProcessInTheGuestInitsGroupEnds(t *testing.T) {
	id, _ := plainSession(t)
	pidf := filepath.Join(t.TempDir(), "child.pid")
	hostileDriver(t, initChild, pidf)
	s, _, ended := startPlain(t, id)
	var child int
	waitFor(t, "the child's pid", func() bool {
		b, err := os.ReadFile(pidf)
		child, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil && child > 0
	})
	pinned(t, child, "sleep")
	s.breach(errors.New("a test's breach"))
	_ = endedWithin(t, ended)
	goneWithin(t, 5*time.Second, child)
}
