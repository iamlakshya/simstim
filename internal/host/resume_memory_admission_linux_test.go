//go:build linux

package host

import (
	"context"
	"testing"
)

func TestPreparationDataCannotIssueNativeAccountingLease(t *testing.T) {
	ctx := context.Background()
	if lease, err := beginResumePreparationV1(ctx, nil, nil, &Manifest{}, "copied-token"); err == nil || lease != nil {
		t.Fatal("data minted an original bridge/native preparation")
	}
	var zero resumePreparationLeaseV1
	if err := zero.Check(ctx, nil, nil); err == nil {
		t.Fatal("zero source checked as a real reservation")
	}
	if err := zero.Cancel(ctx, nil, nil); err == nil {
		t.Fatal("zero source cleared a native preparation")
	}
	if err := takeoverResumePreparationNativeLockedV1(ctx, &SessionOperation{}, nil, &placementControl{}); err == nil {
		t.Fatal("Starting/control data became actual native takeover")
	}
	if err := checkResumePreparationStartLockedV1(ctx, &SessionOperation{}, &Manifest{}, &placementControl{}); err == nil {
		t.Fatal("data issued native startup without the original lease")
	}
}
