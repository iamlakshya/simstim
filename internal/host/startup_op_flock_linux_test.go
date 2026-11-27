//go:build linux

package host

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestStartupLoanRequiresTheOriginalAlreadyLockedDescription(t *testing.T) {
	path := filepath.Join(t.TempDir(), "op.lock")
	parent, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Close() })
	if err := unix.Flock(int(parent.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	fd, err := unix.FcntlInt(parent.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	borrow := os.NewFile(uintptr(fd), "actual-operation-borrow")
	t.Cleanup(func() { _ = borrow.Close() })
	separate, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer separate.Close()
	sender := startupCredentials{pid: os.Getpid(), uid: uint32(os.Getuid()), gid: uint32(os.Getgid())}
	if err := checkBorrowedFlockV1(borrow, sender); err != nil {
		t.Fatalf("same native description: %v", err)
	}
	if err := checkBorrowedFlockV1(separate, sender); err == nil {
		t.Fatal("an independently reopened same-inode descriptor became an OP loan")
	}
	if err := unix.Flock(int(separate.Fd()), unix.LOCK_EX|unix.LOCK_NB); !errors.Is(err, unix.EWOULDBLOCK) {
		t.Fatalf("rejected substitution changed the original lock: %v", err)
	}
	if err := borrow.Close(); err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(separate.Fd()), unix.LOCK_EX|unix.LOCK_NB); !errors.Is(err, unix.EWOULDBLOCK) {
		t.Fatalf("borrowed close released the live parent description: %v", err)
	}
	if err := parent.Close(); err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(separate.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatalf("last reference did not release the lock: %v", err)
	}
}

func TestStartupLoanNeverAcquiresAnUnlockedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "op.lock")
	unlocked, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer unlocked.Close()
	sender := startupCredentials{pid: os.Getpid(), uid: uint32(os.Getuid()), gid: uint32(os.Getgid())}
	if err := checkBorrowedFlockV1(unlocked, sender); err == nil {
		t.Fatal("an unlocked file became authority")
	}
	other, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := unix.Flock(int(other.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatalf("the check acquired a new lock while refusing the loan: %v", err)
	}
}

func TestStartupLoanRejectsAnotherNativeLockSender(t *testing.T) {
	file, err := os.OpenFile(filepath.Join(t.TempDir(), "op.lock"), os.O_CREATE|os.O_RDWR|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if err := checkBorrowedFlockV1(file, startupCredentials{pid: os.Getpid() + 1, uid: uint32(os.Getuid())}); err == nil {
		t.Fatal("a different current native sender inherited the original outside grant")
	}
}
