//go:build linux

package host

import (
	"context"
	"testing"
)

func TestSlotRetryDataCannotIssueFailedCreationCustody(t *testing.T) {
	var source SlotSnapshotOwnerV1
	if err := source.retryColdForRewindV1(context.Background(), nil); err == nil {
		t.Fatal("data issued a slot restart")
	}
	if err := source.checkColdRetrySourceV2(context.Background(), nil); err == nil {
		t.Fatal("data issued failed creator vacancy")
	}
	if err := source.abandonFailedRootV2(context.Background(), nil); err == nil {
		t.Fatal("data invoked reserved abandonment")
	}
	actual := &slotRetryOwnerV2{}
	actual.issuer = actual
	copied := &slotRetryOwnerV2{issuer: actual}
	if err := source.abandonFailedRootV2(context.Background(), copied); err == nil {
		t.Fatal("a copied retry invoked actual root cleanup")
	}
	old := &ColdResumeV1{}
	receipt := &rewindFailedCreationV1{previous: old}
	if receipt.previousColdV1() != old {
		t.Fatal("actual previous creation was substituted")
	}
	if err := receipt.checkV1(context.Background(), nil); err == nil {
		t.Fatal("previous pointer alone issued a retry")
	}
}
