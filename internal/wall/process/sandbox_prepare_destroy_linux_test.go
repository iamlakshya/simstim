//go:build linux

package process

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/wall"
	"golang.org/x/sys/unix"
)

// This exercises the concrete production Destroy against an actual retained
// child pidfd and its actual SIGKILL/wait result. It does not start a namespace
// or VM, and cannot qualify the full preparation boot or parent-death path.
func TestPrepareDestroyAcceptsActualSIGKILLOnlyAfterOwnedPhysicalJoin(t *testing.T) {
	command, _, original := ownedChild(t)
	pidfd, err := unix.FcntlInt(uintptr(original), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	runtimePath := filepath.Join(dir, "process.json")
	sentinel := filepath.Join(dir, "manifest.json")
	for _, path := range []string{runtimePath, sentinel} {
		if err := os.WriteFile(path, []byte("owned"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	prepared, err := newSandbox(wall.Spec{SessionDir: dir})
	if err != nil {
		unix.Close(pidfd)
		t.Fatal(err)
	}
	runtime := prepared.(*sandbox)
	runtime.cmd, runtime.pidfd = command, pidfd
	if err := runtime.Destroy(); err == nil {
		t.Fatal("Destroy accepted the still-live original child")
	}
	if _, err := os.Stat(runtimePath); err != nil {
		t.Fatalf("negative Destroy removed actual runtime storage: %v", err)
	}
	go runtime.reap(command)
	if err := runtime.Kill(); err != nil {
		t.Fatal(err)
	}
	exit := runtime.Wait()
	if exit.Err == nil || !strings.EqualFold(exit.Signal, "killed") || runtime.CleanExit() {
		t.Fatalf("forced native exit diagnostics were lost: %+v", exit)
	}
	if command.ProcessState == nil || !runtime.initProven || !runtime.closed || runtime.pidfd >= 0 {
		t.Fatal("actual original child and pidfd were not physically joined")
	}
	if err := runtime.Destroy(); err != nil {
		t.Fatalf("owned physical cleanup was blocked by a preserved SIGKILL diagnostic: %v", err)
	}
	if _, err := os.Stat(runtimePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned runtime storage remains: %v", err)
	}
	if body, err := os.ReadFile(sentinel); err != nil || string(body) != "owned" {
		t.Fatalf("driver Destroy touched session metadata: %q, %v", body, err)
	}
}
