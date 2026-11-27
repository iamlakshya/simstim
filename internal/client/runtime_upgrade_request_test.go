package client

import "testing"

func TestRuntimeMaintenancePurposeCannotDonateOtherGrants(t *testing.T) {
	r := NewRuntimeUpgradeGrantRequest()
	ctx := WithGrantRequest(t.Context(), r)
	defer r.Close()
	if err := requireRuntimeUpgradeContext(ctx); err != nil {
		t.Fatal(err)
	}
	if requireRemoteGrantRequest(ctx) == nil || requireGrantOperation(ctx, grantOpNew) == nil || requireLocalOperation(ctx, grantLocalRoot, grantOpRootStep) == nil {
		t.Fatal("runtime maintenance donated a remote start or root purpose")
	}
	for _, kind := range []OutboundKind{OutboundSecret, OutboundRepo, OutboundPool, OutboundRootRun, OutboundSilentUpgrade} {
		if requirePurposeOutbound(ctx, r, Outbound{Kind: kind}) == nil {
			t.Fatal("an unsealed runtime request granted an outbound action", kind)
		}
	}
}
