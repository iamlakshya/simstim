package host

import (
	"context"
	"os"
	"runtime"
	"testing"

	"github.com/iamlakshya/simstim/internal/supply"
)

func TestNoneIncompleteMetadataCaptureCannotRemove(t *testing.T) {
	plan := &noneMetadataCleanupV1{}
	plan.self = plan
	result := DeleteResult{}
	if err := plan.Remove(context.Background(), &result); err == nil {
		t.Fatal("an incomplete capture admitted removal")
	}
	if plan.started || result.Deleted {
		t.Fatal("an incomplete capture changed storage or reported deletion")
	}
}

func TestNoneMetadataFailedRootCaptureKeepsActualRoot(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	plan := &noneMetadataCleanupV1{}
	plan.self = plan
	if err := plan.addRootV1("member", root); err == nil {
		t.Fatal("a closed native root supplied a completed capture")
	}
	if len(plan.pins) != 1 || plan.pins[0].root != root || plan.ready {
		t.Fatal("failed capture discarded its actual root before its owner could close")
	}
}

func TestNoneMetadataPreparationCloseKeepsOnlyUnsettledResources(t *testing.T) {
	// Keep this observation and the real retirement attempt on the SAME thread.
	// Unsupported profiles must retain roots; this is no portable Linux proof.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	supported := supply.CheckFileCloseThreadV1() == nil
	primary, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer primary.Close()
	parent, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	member, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := member.Close(); err != nil {
		t.Fatal(err)
	}
	plan := &noneMetadataCleanupV1{
		operation: &SessionOperation{native: &sessionFileLease{root: primary}},
		parent:    parent,
		pins:      []noneDeleteRootV1{{id: "member", root: member}, {id: "primary", root: primary}},
	}
	plan.self = plan
	if err := plan.Close(); err == nil {
		t.Fatal("an unconfirmed native close reported success")
	}
	if plan.closed || plan.pins[0].root != member || plan.pins[1].root != primary {
		t.Fatal("partial closure lost an unresolved owner or the original operation root")
	}
	if supported {
		if plan.parent != nil {
			t.Fatal("a known-retired parent stayed in unsettled custody")
		}
		if _, err := parent.Stat("."); err == nil {
			t.Fatal("the original known-retired parent remained open")
		}
	} else {
		if plan.parent != parent {
			t.Fatal("an unsupported close profile discarded its original parent")
		}
		if _, err := parent.Stat("."); err != nil {
			t.Fatal("an unsupported profile attempted to close its original parent", err)
		}
		defer parent.Close()
	}
	first := plan.retirement.roots[member]
	if first == nil || first.root != member {
		t.Fatal("the failed close has no retained original first-call record")
	}
	if err := plan.Close(); err == nil || plan.closed {
		t.Fatal("retry inferred closure from a prior error")
	}
	if plan.retirement.roots[member] != first || plan.pins[0].root != member {
		t.Fatal("retry replaced the unresolved native owner or its first-close facts")
	}

	// Lower resource guard only: these real file/root objects issue no Source.
	// An unconfirmed first-lease root must not be hidden by generic Close.once.
	leaseRoot, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	leaseFile, err := leaseRoot.OpenFile("first.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		leaseRoot.Close()
		t.Fatal(err)
	}
	defer leaseFile.Close()
	if err := leaseRoot.Close(); err != nil {
		t.Fatal(err)
	}
	lease := &sessionFileLease{root: leaseRoot, file: leaseFile}
	firstPlan := &noneMetadataCleanupV1{first: lease}
	firstPlan.self = firstPlan
	if err := firstPlan.Close(); err == nil || firstPlan.closed || firstPlan.first != lease {
		t.Fatal("unconfirmed first-lease retirement discarded its original resources")
	}
	if err := firstPlan.Close(); err == nil || firstPlan.closed || firstPlan.first != lease {
		t.Fatal("first-lease retry used once completion as native retirement")
	}
}
