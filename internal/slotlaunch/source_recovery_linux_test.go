//go:build linux

package slotlaunch

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// These fixtures use unprivileged process birth handles only. They exercise
// refusal and retry, and do not claim successful native cgroup recovery.
func TestKeeperRecoveryNeedsPositiveOriginalExit(t *testing.T) {
	fd, err := unix.PidfdOpen(os.Getpid(), 0)
	if err != nil {
		t.Fatal(err)
	}
	live := os.NewFile(uintptr(fd), "live-original-process")
	defer live.Close()
	ordinary, err := os.CreateTemp(t.TempDir(), "ordinary")
	if err != nil {
		t.Fatal(err)
	}
	defer ordinary.Close()
	closed, err := os.CreateTemp(t.TempDir(), "closed")
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	for _, handle := range []*os.File{nil, ordinary, closed, live} {
		files := make([]*os.File, 13)
		files[0] = handle
		source := &NativeSourceV1{adopted: true, owner: context.Background(), files: files}
		if recover, err := source.orphanRecoveryNeeded(); recover || err != nil {
			t.Fatalf("unknown or live process triggered recovery: %v, %v", recover, err)
		}
	}
}

func TestExitedKeeperRecoveryRefusalRetainsOriginalCapsuleForRetry(t *testing.T) {
	command := exec.Command("/bin/true")
	fd := -1
	command.SysProcAttr = &syscall.SysProcAttr{PidFD: &fd}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	handle := os.NewFile(uintptr(fd), "exited-original-process")
	defer handle.Close()
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	if !pidExited(handle) {
		t.Fatal("original creation handle did not positively report exit")
	}
	boot, err := nativeBootID()
	if err != nil {
		t.Fatal(err)
	}
	files := make([]*os.File, 13)
	files[0] = handle
	source := &NativeSourceV1{adopted: true, owner: context.Background(), boot: boot, files: files}
	for range 2 {
		if recover, err := source.orphanRecoveryNeeded(); recover || err == nil {
			t.Fatal("exited keeper without its original cgroup and parent authorised recovery")
		}
		if source.closed || !source.adopted || source.files[0] != handle || !pidExited(handle) {
			t.Fatal("refusal closed or replaced the original capsule")
		}
	}
}
