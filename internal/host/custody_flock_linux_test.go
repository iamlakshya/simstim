//go:build linux

package host

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestCustodyFlockObservesOriginalDescriptionWithoutAcquiring(t *testing.T) {
	name := filepath.Join(t.TempDir(), "holder.lock")
	original, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	if err := unix.Flock(int(original.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	fd, err := unix.FcntlInt(original.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	alias := os.NewFile(uintptr(fd), "same-holder-ofd")
	defer alias.Close()
	separate, err := os.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer separate.Close()
	if err := checkCustodyFlockV1(alias); err != nil {
		t.Fatal("same original description refused", err)
	}
	if err := checkCustodyFlockV1(separate); err == nil {
		t.Fatal("reopened same inode became native custody")
	}
	if err := original.Close(); err != nil {
		t.Fatal(err)
	}
	if err := checkCustodyFlockV1(alias); err != nil {
		t.Fatal("closing another alias ended the original OFD", err)
	}
	if err := unix.Flock(int(separate.Fd()), unix.LOCK_EX|unix.LOCK_NB); !errors.Is(err, unix.EWOULDBLOCK) {
		t.Fatal("observer released the original lock", err)
	}
	if err := alias.Close(); err != nil {
		t.Fatal(err)
	}
	if err := checkCustodyFlockV1(separate); err == nil {
		t.Fatal("observer minted custody on an unlocked description")
	}
	if err := unix.Flock(int(separate.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal("refusal acquired a new lock", err)
	}
}

func TestCustodyFlockSurvivesExitedOriginalLockerWithoutBecomingLiveOPLoan(t *testing.T) {
	if os.Getenv("NATIVE_CUSTODY_FLOCK_HELPER") == "1" {
		if err := unix.Flock(3, unix.LOCK_EX|unix.LOCK_NB); err != nil {
			t.Fatal(err)
		}
		return
	}
	name := filepath.Join(t.TempDir(), "holder.lock")
	original, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	command := exec.Command(os.Args[0], "-test.run=^TestCustodyFlockSurvivesExitedOriginalLockerWithoutBecomingLiveOPLoan$")
	command.Env = append(os.Environ(), "NATIVE_CUSTODY_FLOCK_HELPER=1")
	command.ExtraFiles = []*os.File{original}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("original native locker: %s: %v", output, err)
	}
	if err := checkCustodyFlockV1(original); err != nil {
		t.Fatal("the SAME OFD lost custody when its original locker exited", err)
	}
	if err := checkBorrowedFlockV1(original, startupCredentials{pid: os.Getpid(), uid: uint32(os.Getuid())}); err == nil {
		t.Fatal("the expired original locker became this live OP sender")
	}
	separate, err := os.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer separate.Close()
	if err := checkCustodyFlockV1(separate); err == nil {
		t.Fatal("a same-inode reopen adopted the exited locker's original OFD")
	}
	if err := unix.Flock(int(separate.Fd()), unix.LOCK_EX|unix.LOCK_NB); !errors.Is(err, unix.EWOULDBLOCK) {
		t.Fatal("the real retained description lost its kernel lock", err)
	}
	if err := original.Close(); err != nil {
		t.Fatal(err)
	}
	if err := checkCustodyFlockV1(separate); err == nil {
		t.Fatal("an expired original description renewed custody")
	}
	if err := unix.Flock(int(separate.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal("observation acquired or leaked a new lock", err)
	}
}
