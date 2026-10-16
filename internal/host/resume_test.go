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

// Without systemd, a holder that answers neither the kill request nor
// SIGTERM gets SIGKILL. stopHolder returns only once it is gone, so that
// its last save and its removal of the socket cannot follow a new start.
func TestStopHolderWaitsForAStuckHolder(t *testing.T) {
	alertHome(t)
	run, err := os.MkdirTemp("", "rs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(run) })
	t.Setenv("XDG_RUNTIME_DIR", run)
	old := stopWait
	stopWait = 300 * time.Millisecond
	t.Cleanup(func() { stopWait = old })

	// The socket answers, but nothing on it stops, as a hung holder.
	id := "stuck234"
	if err := paths.EnsurePrivateDir(filepath.Dir(paths.Socket(id))); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", paths.Socket(id))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	hung := exec.Command("/bin/sh", "-c", `trap "" TERM; while :; do sleep 1; done`)
	if err := hung.Start(); err != nil {
		t.Fatal(err)
	}
	pid := hung.Process.Pid
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	time.Sleep(100 * time.Millisecond) // for the trap to be set

	stopHolder(id, &Manifest{ID: id, Run: Runtime{HolderPID: pid}})
	if err := syscall.Kill(pid, 0); err == nil {
		t.Error("the stuck holder still runs after stopHolder")
	}
	if _, err := os.Stat(paths.Socket(id)); err == nil {
		t.Error("the stuck holder's socket is still there")
	}
}
