package host

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/paths"
)

// The session's unit stops, and the new holder waits until systemd has let
// go of the unit's name.
func TestStopUnitWaitsForSystemd(t *testing.T) {
	calls := fakeSystemctl(t, "not-found\n", nil)
	stopUnit("s-abc.service")
	want := [][]string{{"stop", "s-abc.service"}, {"show", "s-abc.service", "-p", "LoadState", "--value"}}
	if !slices.EqualFunc(*calls, want, slices.Equal[[]string]) {
		t.Fatalf("systemctl calls = %q, want %q", *calls, want)
	}
}

// An unacknowledged holder cannot be forced to stop through a manifest PID.
// The recorded PID may now belong to another process. Recovery waits for a
// bounded time, keeps the socket and leaves that process alone.
func TestStopHolderWaitsForAStuckHolder(t *testing.T) {
	secretHome(t)
	old := stopWait
	stopWait = 300 * time.Millisecond
	t.Cleanup(func() { stopWait = old })
	id := "stuck234"
	if err := paths.EnsurePrivateDir(filepath.Dir(paths.Socket(id))); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", paths.Socket(id))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	// This benign child belongs to the test, not the session. No user
	// process is selected. Cleanup owns its exact process handle.
	other := exec.Command("sleep", "60")
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = other.Process.Kill()
		_, _ = other.Process.Wait()
	})
	started := time.Now()
	if err := stopHolder(id, &Manifest{ID: id, Run: Runtime{HolderPID: other.Process.Pid}}); err == nil {
		t.Fatal("an unacknowledged holder was reported stopped")
	}
	if elapsed := time.Since(started); elapsed < stopWait || elapsed > 3*time.Second+2*stopWait {
		t.Errorf("stop observation took %v outside its bounded wait", elapsed)
	}
	if err := other.Process.Signal(syscall.Signal(0)); err != nil {
		t.Errorf("recovery signalled the recorded unrelated process: %v", err)
	}
	if _, err := os.Stat(paths.Socket(id)); err != nil {
		t.Errorf("the unacknowledged holder's socket was removed: %v", err)
	}
}
