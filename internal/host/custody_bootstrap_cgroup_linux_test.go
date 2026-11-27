//go:build linux

package host

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestPassiveCgroupReuseCannotRunAfterPossibleFactoryRelease(t *testing.T) {
	for _, launch := range []*startupLaunchV1{nil, {}, {custodyReleaseSent: true}, {ready: true}} {
		source := &custodyKernelSourceV1{launch: launch}
		if err := source.checkPassiveOldClosureV1(context.Background()); err == nil {
			t.Fatal("a missing or dispatched bootstrap reached the cgroup reuse exception")
		}
	}
	inherited := &custodyInheritanceV1{}
	inherited.released.Store(true)
	if err := inherited.checkPassiveOldClosureV1(context.Background()); err == nil {
		t.Fatal("a released inherited main reacquired pre-factory cgroup authority")
	}
}

func TestBootstrapCgroupDataCannotReplaceOriginalKernelGroup(t *testing.T) {
	pidfd := -1
	cmd := exec.Command("/bin/true")
	cmd.SysProcAttr = &syscall.SysProcAttr{PidFD: &pidfd}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if pidfd < 0 {
		t.Fatal("kernel did not supply original atomic process handle")
	}
	old := os.NewFile(uintptr(pidfd), "actual-exited-test-creator")
	defer old.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := checkCustodyExitedMainV1(old); err != nil {
		t.Fatal("actual original native child did not exit", err)
	}
	root := t.TempDir()
	if err := os.WriteFile(root+"/cgroup.procs", []byte("1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root+"/cgroup.events", []byte("populated 0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	group, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer group.Close()
	if err := custodyBootstrapCgroupFactsV1(old, group, group, os.Getpid()); err == nil {
		t.Fatal("filesystem rows impersonated a native original/new cgroup")
	}
}

func TestBootstrapOriginalExitRejectsReadableNonProcessObject(t *testing.T) {
	fd, err := unix.Eventfd(1, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(fd), "readable-test-event")
	defer file.Close()
	if err := checkCustodyExitedMainV1(file); err == nil {
		t.Fatal("readiness from another native object replaced original main exit")
	}
}
