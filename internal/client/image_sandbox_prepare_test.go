package client

import (
	"context"
	"testing"
)

func TestSandboxPrepareImageNeedsItsActualPlanAndPhase(t *testing.T) {
	for _, mode := range []string{"no-request", "ordinary-new", "missing-prepare-plan"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			switch mode {
			case "ordinary-new":
				ctx = WithGrantRequest(ctx, NewGrantRequest(false, false))
				if err := requireGrantOperation(ctx, grantOpNew); err != nil {
					t.Fatal(err)
				}
			case "missing-prepare-plan":
				r := &GrantRequest{purpose: grantHostPrepare, operation: grantOpSandboxPrepare}
				ctx = WithGrantRequest(ctx, r)
			}
			if _, err := sandboxPrepareImagePhaseV1(ctx, nil); err == nil {
				t.Fatal("metadata or another family invented a fixed image preparation phase")
			}
			if _, err := captureSandboxPrepareImagePolicyV1(ctx, nil); err == nil {
				t.Fatal("missing original plan opened a replacement image source")
			}
		})
	}
}

func TestSandboxPrepareImageCannotFallBackToOrdinarySettings(t *testing.T) {
	r := &GrantRequest{purpose: grantHostPrepare, operation: grantOpSandboxPrepare}
	ctx := WithGrantRequest(context.Background(), r)
	if _, _, close, err := withRetainedImagePolicy(ctx, nil); err == nil {
		_ = close()
		t.Fatal("fixed host prepare opened ordinary image policy without its plan")
	}
	if r.imagePolicy != nil || r.imageFetch != nil {
		t.Fatal("refused phase attached source or fetch ownership")
	}
}
