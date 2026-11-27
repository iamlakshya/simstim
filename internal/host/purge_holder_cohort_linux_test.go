//go:build linux

package host

import (
	"context"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func holderCohortFixtureV3(t *testing.T, receiver, sender *os.File) *ordinaryHolderCohortV3 {
	t.Helper()
	owner := &ordinaryHolderCohortV3{receiver: duplicatePurgeDecisionFixtureV3(t, receiver), sender: duplicatePurgeDecisionFixtureV3(t, sender)}
	var err error
	owner.receiverInfo, err = owner.receiver.Stat()
	if err != nil {
		t.Fatal(err)
	}
	owner.senderInfo, err = owner.sender.Stat()
	if err != nil {
		t.Fatal(err)
	}
	owner.receiverCookie, err = unix.GetsockoptUint64(int(owner.receiver.Fd()), unix.SOL_SOCKET, unix.SO_COOKIE)
	if err != nil {
		t.Fatal(err)
	}
	owner.senderCookie, err = unix.GetsockoptUint64(int(owner.sender.Fd()), unix.SOL_SOCKET, unix.SO_COOKIE)
	if err != nil {
		t.Fatal(err)
	}
	return owner
}

func TestOrdinaryHolderDeathAbortFollowsAlreadyQueuedCommitV3(t *testing.T) {
	receiver, sender := nativePurgeDecisionFixtureV3(t)
	holder := holderCohortFixtureV3(t, receiver, sender)
	if first, err := proposePurgeCohortDecisionV3(receiver, sender, purgeCohortCommitV3); err != nil || first != purgeCohortCommitV3 {
		t.Fatal(first, err)
	}
	// The very method used by the real holder's EOF/death path must follow
	// COMMIT. Its abort proposal cannot grant local restoration permission.
	for range 4 {
		if first, err := holder.abortV3(); err != nil || first != purgeCohortCommitV3 {
			t.Fatal("parent loss replaced the original global commit", first, err)
		}
	}
}

func TestOrdinaryHolderAbortIsSharedWithLateMonitorV3(t *testing.T) {
	receiver, sender := nativePurgeDecisionFixtureV3(t)
	holder := holderCohortFixtureV3(t, receiver, sender)
	if first, err := holder.abortV3(); err != nil || first != purgeCohortAbortV3 {
		t.Fatal(first, err)
	}
	late := holderCohortFixtureV3(t, receiver, sender)
	if first, err := proposePurgeCohortDecisionV3(late.receiver, late.sender, purgeCohortCommitV3); err != nil || first != purgeCohortAbortV3 {
		t.Fatal("late readiness replaced the original holder abort", first, err)
	}
}

func TestOrdinaryHolderUnknownNativeDecisionCannotRestoreV3(t *testing.T) {
	receiver, sender := nativePurgeDecisionFixtureV3(t)
	holder := holderCohortFixtureV3(t, receiver, sender)
	if _, err := unix.SendmsgN(int(sender.Fd()), []byte{PurgeCohortAbortByteV3, PurgeCohortCommitByteV3}, nil, nil, unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL); err != nil {
		t.Fatal(err)
	}
	if first, err := holder.abortV3(); err == nil || first != purgeCohortPendingV3 {
		t.Fatal("malformed first packet supplied restoration permission", first, err)
	}
	other, _ := nativePurgeDecisionFixtureV3(t)
	holder.receiver = other
	if first, err := holder.firstV3(); err == nil || first != purgeCohortPendingV3 {
		t.Fatal("substituted native socket supplied a cohort decision", first, err)
	}
}

func TestOrdinaryCohortCannotBindFromDataOrAnUnreadyPreparationV3(t *testing.T) {
	for _, preparation := range []*OrdinaryPurgePreparationV2{nil, {}, {owner: &ordinaryHolderCaptureV2{}}} {
		if err := preparation.bindCohortV3(context.Background(), &PurgeCohortDecisionV3{}); err == nil {
			t.Fatal("data-only original-operation labels installed a native monitor")
		}
	}
}
