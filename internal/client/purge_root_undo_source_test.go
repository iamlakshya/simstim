package client

import (
	"context"
	"errors"
	"testing"
)

func TestPurgeFixedRootUndoRequiresOriginalPurgeAndPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := RunPurgeRootUndo(ctx, RootRun, RootRunOptions{Passwordless: true}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation became a root execution or another error", err)
	}
	for _, request := range []*GrantRequest{NewGrantRequest(false, false), NewRootStepGrantRequest(), NewPurgeGrantRequest()} {
		ctx := WithGrantRequest(t.Context(), request)
		if err := RunPurgeRootUndo(ctx, RootRun, RootRunOptions{Passwordless: true}); err == nil {
			t.Fatal("a request without the actual original purge acquired native root undo")
		}
		if err := requirePurgeRootOutboundV1(ctx, request, Outbound{Kind: OutboundRootRun, Name: "arbitrary-history-hash"}); err == nil {
			t.Fatal("a public root history hash created an execution grant")
		}
	}
}
