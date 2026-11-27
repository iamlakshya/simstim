package host

import (
	"context"
	"testing"
)

func TestWarmWorkerCannotOriginateFromCopiedOrEndedOperation(t *testing.T) {
	owner := &OwnedSupervisorLease{}
	line := &StartupLineV1{reading: true}
	for _, operation := range []*SessionOperation{nil, {id: "parent23"}, {id: "parent23", native: &sessionFileLease{borrowed: true}}} {
		if operation != nil {
			origin := &warmIncomingOriginV1{owner: owner, operation: operation, native: operation.native, line: line}
			origin.issuer = origin
			operation.warmIncoming = origin
		}
		if err := owner.CheckWarmOperationV1(t.Context(), line, operation); err == nil {
			t.Fatal("copied correlation or an ended lease issued an asynchronous native producer")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := owner.CheckWarmOperationV1(ctx, line, nil); err == nil {
		t.Fatal("cancelled worker acquired native custody")
	}
}

func TestCurrentFrameCredentialGuardRemainsSeparateFromWorkerOrigin(t *testing.T) {
	line := &StartupLineV1{cred: &startupCredentials{pid: 123, uid: 456}, reading: true}
	if _, err := line.currentSender(); err == nil {
		t.Fatal("the next physical read exposed credentials from an earlier frame")
	}
	// No producer is issued by manipulating that framing state. The positive
	// native oracle must start/commit an actual supervisor, accept its genuine
	// incoming Warm OP, then advance ReadFrame; it cannot set boot.ready here.
	if err := (&OwnedSupervisorLease{}).CheckWarmSenderV1(line); err == nil {
		t.Fatal("frame data supplied the original native actor")
	}
}
