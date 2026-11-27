package host

import (
	"errors"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestPendingResumePrebindOwnerRequiresAcknowledgedCancellation(t *testing.T) {
	m := startupTestManifest(t)
	release, err := acquireHolderLock(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	err = stopPendingForResume(m.ID, m)
	var refusal *proto.Error
	if !errors.As(err, &refusal) || refusal.Code != "busy" || !holderLockHeld(m.ID) {
		t.Fatalf("pre-bind owner bypassed its pending cancellation: %v, lock %v", err, holderLockHeld(m.ID))
	}
	if err := stopResumeHolder(m.ID, m, true); err == nil {
		t.Fatal("pre-bind owner reached the ordinary stop path")
	}
}

func TestPendingResumeAbsentOwnerNeedsNoGenericStop(t *testing.T) {
	m := startupTestManifest(t)
	if err := stopPendingForResume(m.ID, m); err != nil {
		t.Fatalf("an absent pending owner prevented recovery: %v", err)
	}
	if err := stopResumeHolder(m.ID, m, true); err != nil {
		t.Fatal(err)
	}
}

func TestPendingResumeRefusesTheRunningOwnerWithoutStoppingIt(t *testing.T) {
	m, p, _ := startupControlFixture(t)
	h := &Holder{id: m.ID, m: m, log: p.peer.log, clients: map[*hclient]struct{}{}}
	p.prepare(h, false)
	if err := p.beginAgent(); err != nil {
		t.Fatal(err)
	}
	p.activate(h)
	err := stopPendingForResume(m.ID, m)
	var refusal *proto.Error
	if !errors.As(err, &refusal) || refusal.Code != "agent_running" || p.ctx.Err() != nil || !holderLockHeld(m.ID) {
		t.Fatalf("running owner was treated as pending: %v, cancelled %v", err, p.ctx.Err())
	}
}

func TestPendingResumeJoinsTheAcknowledgedPrelaunchOwner(t *testing.T) {
	m, p, finish := startupControlFixture(t)
	worker := ownedStartupWorker(t, func() error { return stopPendingForResume(m.ID, m) }, finish)
	awaitStartupTest(t, p.ctx.Done())
	finish()
	if err := worker.wait(t); err != nil {
		t.Fatal(err)
	}
	assertPendingStopReleased(t, m.ID)
}

func TestPendingResumeUnknownUnitIsNotAbsenceAuthority(t *testing.T) {
	m := startupTestManifest(t)
	m.Run.Unit = "another.service"
	if err := stopPendingForResume(m.ID, m); err == nil {
		t.Fatal("an unconfirmed recorded unit became absence authority")
	}
}
