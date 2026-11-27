package client

import (
	"context"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestPurgeRequestRequiresOriginalOwnerEvenWithForce(t *testing.T) {
	for _, op := range []string{"host.purge", "host.purge.commit"} {
		if err := purgeBeforeRequest(context.Background(), &Conn{}, proto.Request{Op: op}, api.PurgeArgs{Force: true}); err == nil {
			t.Fatal("a request without its original owner reached purge publication", op)
		}
	}
}

func TestPurgeLoginResultsCannotSelectAnotherFile(t *testing.T) {
	paths := []string{"~/.claude/.credentials.json", "~/.codex/auth.json"}
	rows, err := parsePurgeLoginRowsV1([]byte("0\tpresent\n1\tabsent\n"), paths, false)
	if err != nil || len(rows) != 1 || rows[0] != paths[0] {
		t.Fatal(rows, err)
	}
	for _, raw := range []string{"1\tpresent\n0\tabsent\n", "0\tpresent\n0\tpresent\n", "0\tpresent\n1\tabsent\n2\tpresent\n", "0\tremoved\n1\tabsent\n", "0\tpresent\n"} {
		if _, err := parsePurgeLoginRowsV1([]byte(raw), paths, false); err == nil {
			t.Fatal("unknown login result became a fixed credential decision", raw)
		}
	}
	if _, err := parsePurgeLoginRowsV1([]byte("0\tpresent\n1\tabsent\n"), paths, true); err == nil {
		t.Fatal("a delete response silently retained a selected ordinary file")
	}
}

func TestPurgeStagesDoNotSkipRootOrLoginDecision(t *testing.T) {
	for _, done := range [][]string{nil, {"host"}, {"host", "root"}, {"host", "root", "logins", "baseline"}} {
		receipt := purgeRemovalReceiptV1{Done: done}
		if receipt.readyForGuard() || receipt.complete() {
			t.Fatal("an unfinished local decision reached final guard cleanup", done)
		}
	}
	if !(purgeRemovalReceiptV1{Done: []string{"host", "root", "logins", "baseline", "rotate"}}).readyForGuard() {
		t.Fatal("a complete local prefix did not reach acknowledgement")
	}
}

func TestPurgeCloseRetainsActualInflightOwner(t *testing.T) {
	owner := &purgeOperationV1{active: true}
	release, err := owner.retainNativeCallV1()
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.close(); err == nil || !owner.active {
		t.Fatal("a live native call lost its original request owner")
	}
	release()
	release()
	if err := owner.close(); err != nil || owner.active {
		t.Fatal("a joined native call could not close", err)
	}
}

func TestPurgeRetryForceChangesOnlyAnUnfinishedWorkLossDecision(t *testing.T) {
	before := purgeRemovalReceiptV1{RootMode: RootPrint, Done: []string{}}
	after, err := purgeRetryDecisionsV1(before, true, RootPrint, false)
	if err != nil || !after.Force || before.Force {
		t.Fatal("a fresh explicit force decision lost its original immutable receipt", after, err)
	}
	before.Done = []string{"host"}
	if _, err := purgeRetryDecisionsV1(before, true, RootPrint, false); err == nil {
		t.Fatal("force changed a completed native host stage")
	}
	if _, err := purgeRetryDecisionsV1(purgeRemovalReceiptV1{RootMode: RootPrint, RootAttempted: true}, true, RootRun, false); err == nil {
		t.Fatal("a root mode change replaced an already dispatched root attempt")
	}
	narrowed, err := purgeRetryDecisionsV1(purgeRemovalReceiptV1{RootMode: RootRun}, false, RootSkip, false)
	if err != nil || narrowed.RootMode != RootSkip {
		t.Fatal("a new explicit keep decision could not narrow an undispatched root step", err)
	}
}
