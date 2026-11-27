package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
)

// Exercise the actual member constructor and real directory reservation. No
// holder, namespace, provider, unit or fabricated physical stop starts here.
func TestActualMemberConstructorRetainsOriginalGroupRoots(t *testing.T) {
	isolatedStoreHostConstruction(t)
	const primaryID, childID = "parent234", "child234"
	parent := &Manifest{Schema: 1, ID: primaryID, Name: "primary", CreatedAt: time.Now().UTC(), Wall: &WallRecord{Level: "process", Driver: "plain"}}
	if err := parent.Save(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := SandboxSnapshot(primaryID)
	if err != nil {
		t.Fatal(err)
	}
	start, err := PrepareSandboxMember(parent, api.JoinArgs{Parent: primaryID, ID: childID, Name: "child", Argv: []string{"/usr/bin/true"}, Snapshot: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	defer start.Finish(false)
	var receipt memberRootReceiptV1
	if err := withPlacement(context.Background(), false, func(control *placementControl) error {
		receipt = control.MemberRoots[childID]
		return nil
	}, false); err != nil {
		t.Fatal(err)
	}
	primaryInfo, err := os.Lstat(paths.SessionDir(primaryID))
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Primary != primaryID || !receipt.PrimaryRef.matches(primaryInfo) || !receipt.Directory.matches(start.reservation.created) || !receipt.CreatedAt.Equal(start.Manifest.CreatedAt) {
		t.Fatal("actual member creation did not retain both original native group roots")
	}
	if err := os.Rename(paths.SessionDir(childID), filepath.Join(filepath.Dir(paths.SessionDir(childID)), "moved-original")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(paths.SessionDir(childID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := start.Manifest.Save(); err != nil {
		t.Fatal(err)
	}
	if err := start.recordCreatedMemberRootV1(context.Background(), parent); err == nil {
		t.Fatal("same-name replacement renewed the original member creation receipt")
	}
	if err := withPlacement(context.Background(), false, func(control *placementControl) error {
		current := control.MemberRoots[childID]
		if !samePurgeDirectoryRef(current.Directory, receipt.Directory) || !samePurgeDirectoryRef(current.PrimaryRef, receipt.PrimaryRef) {
			t.Fatal("failed member publication adopted a replacement directory")
		}
		return nil
	}, false); err != nil {
		t.Fatal(err)
	}
}

func TestMemberRootReceiptPreservesAndRejectsFutureOwnership(t *testing.T) {
	var receipt memberRootReceiptV1
	raw := []byte(`{"primary":"parent234","primary_root":{},"directory":{},"created_at":"2026-01-01T00:00:00Z","future_owner":{"id":"unrelated"}}`)
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil || string(fields["future_owner"]) != `{"id":"unrelated"}` {
		t.Fatal("future original member metadata was not retained", err)
	}
	if err := validateMemberRootReceiptsV1(map[string]memberRootReceiptV1{"child234": receipt}); err == nil {
		t.Fatal("future or incomplete metadata authorised a member root")
	}
}
