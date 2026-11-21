package slotlease

import (
	"context"
	"testing"
)

func TestZeroSlotLeaseCannotIssueNativeAuthority(t *testing.T) {
	var source SourceV1
	if source.CheckCurrentV1(context.Background()) == nil {
		t.Fatal("zero lease accepted")
	}
	if file, err := source.RetainForInitV1(); err == nil {
		file.Close()
		t.Fatal("zero lease produced an init descriptor")
	}
	if source.Close() != nil || source.Close() != nil {
		t.Fatal("closing invalid source is not idempotent")
	}
	var absent *SourceV1
	if absent.CheckCurrentV1(context.Background()) == nil {
		t.Fatal("nil lease accepted")
	}
}
