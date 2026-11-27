//go:build linux

package host

import (
	"context"
	"testing"
)

func TestNoneManagerKernelRowsCannotIssueAnOrdinaryOrWalledSource(t *testing.T) {
	managed := &noneManagerCustodyV1{}
	managed.self = managed
	cold := &NoneColdResumeV1{managed: managed}
	cold.self = cold
	managed.cold = cold
	if err := managed.checkPreparedV1(context.Background()); err == nil {
		t.Fatal("data-only managed None issued an ordinary native RSS source")
	}
	if err := managed.checkRetainedCleanupV1(context.Background()); err == nil {
		t.Fatal("data-only managed None issued retained directory cleanup")
	}
	if _, ok := any(managed).(*ColdResumeV1); ok {
		t.Fatal("managed None became a VM artefact cold source")
	}
	if _, ok := any(managed).(*StoppedSessionV1); ok {
		t.Fatal("managed None became whole-unit stopped authority")
	}
	if _, err := captureManagedNoneColdV1(context.Background(), nil, &bridgeResumePreparationV1{}, &Manifest{}); err == nil {
		t.Fatal("copied preparation DATA issued actual original managed None")
	}
}
