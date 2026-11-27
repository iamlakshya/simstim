//go:build linux

package host

import (
	"context"
	"os"
	"testing"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/wall/none"
	"golang.org/x/sys/unix"
)

// Kernel observation guard only: no runtime, stopped object or issuer is
// fabricated by these local file descriptors.
func TestNoneHolderObservationRefusesIndependentSameInodeAndUnlockedFDs(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	original, err := cachefs.OpenFile(root, "holder.lock", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	if err := observeNoneHolderV1(original); err == nil {
		t.Fatal("an unlocked file became an existing Noe holder")
	}
	held, err := cachefs.TryLock(original, cachefs.Exclusive)
	if err != nil || !held {
		t.Fatal(err)
	}
	fd, err := unix.FcntlInt(original.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	alias := os.NewFile(uintptr(fd), "same-owned-test-description")
	defer alias.Close()
	if err := observeNoneHolderV1(alias); err != nil {
		t.Fatal("the same held OFD was refused", err)
	}
	reopen, err := cachefs.OpenFile(root, "holder.lock", os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer reopen.Close()
	if err := observeNoneHolderV1(reopen); err == nil {
		t.Fatal("an independently reopened same inode became the original OFD")
	}
	if err := checkBorrowedFlockV1(alias, startupCredentials{pid: os.Getpid() + 1, uid: uint32(os.Getuid())}); err == nil {
		t.Fatal("None observation weakened the original OP current-sender check")
	}
	shared, err := cachefs.OpenFile(root, "shared.lock", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer shared.Close()
	if held, err := cachefs.TryLock(shared, cachefs.Shared); err != nil || !held {
		t.Fatal(err)
	}
	if err := observeNoneHolderV1(shared); err == nil {
		t.Fatal("a shared description became the original exclusive Noe holder")
	}
}

func TestNoneHolderBirthCannotComeFromForeignOrCopiedData(t *testing.T) {
	if _, err := newNoneHolderBirthLockedV1(context.Background(), &OriginalVMNeed{}, &none.LiveOwnerV1{}, nil); err == nil {
		t.Fatal("a raw receipt created a Noe holder birth")
	}
	original := &noneHolderBirthV1{}
	original.self = original
	copy := *original
	if err := copy.checkLiveLockedV1(context.Background(), &OriginalVMNeed{}, &none.LiveOwnerV1{}); err == nil {
		t.Fatal("a copied holder origin was accepted")
	}
	var absent *noneWarmHolderV1
	if err := absent.checkDescriptionV1(context.Background(), &StoppedSessionV1{}); err == nil {
		t.Fatal("a raw controller DTO became a Noe holder")
	}
	if _, ok := any(original).(*StoppedSessionV1); ok {
		t.Fatal("a Noe birth became whole-stop authority")
	}
}
