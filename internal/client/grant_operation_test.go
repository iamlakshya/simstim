package client

import (
	"errors"
	"maps"
	"testing"
)

func requireScopeRefusal(t *testing.T, err error) {
	t.Helper()
	var refused *UserError
	if !errors.As(err, &refused) || refused.Code != "grant_scope" {
		t.Fatalf("changed selection was not refused as scope: %v", err)
	}
}

func TestGrantRequestCannotSwitchValueOperationOrGrowNewNames(t *testing.T) {
	_, parent, c, r := sharedGrantFixture(t)
	ctx := WithGrantRequest(t.Context(), r)
	if err := c.CheckNewGrants(ctx, []string{"API_KEY"}, false); err != nil {
		t.Fatal(err)
	}
	requireScopeRefusal(t, c.CheckNewGrants(ctx, []string{"API_KEY", "OTHER_KEY"}, false))
	requireScopeRefusal(t, c.CheckNewGrants(ctx, []string{"API_KEY"}, true))
	requireScopeRefusal(t, c.CheckJoinGrants(ctx, parent.SessionID, []string{"API_KEY"}))
	requireScopeRefusal(t, c.CheckResumeGrants(ctx, parent.SessionID, []string{"API_KEY"}))
	r.Untrusted = true
	requireScopeRefusal(t, c.CheckNewGrants(ctx, []string{"API_KEY"}, false))
	r.Untrusted = false
	if err := c.CheckNewGrants(ctx, []string{"API_KEY"}, false); err != nil {
		t.Fatalf("unchanged New retry failed: %v", err)
	}
}

func TestReservationRetryCannotGrowItsOriginalSentNames(t *testing.T) {
	h := h5GrantState(t)
	p := h5Policy()
	if err := reserveSandbox(t.Context(), h, p); err != nil {
		t.Fatal(err)
	}
	p.Secrets = append(p.Secrets, "OTHER_KEY")
	requireScopeRefusal(t, reserveSandbox(t.Context(), h, p))
	if saved, err := LoadSandboxPolicy(t.Context(), h, p.SessionID); err != nil || saved == nil || len(saved.Secrets) != 1 || saved.Secrets[0] != "API_KEY" {
		t.Fatalf("retry grew the original creation names: %+v %v", saved, err)
	}
}

func TestNewRequestKeepsAttachAndPasteContinuation(t *testing.T) {
	_, _, c, r := sharedGrantFixture(t)
	ctx := WithGrantRequest(t.Context(), r)
	if err := c.CheckNewGrants(ctx, nil, false); err != nil {
		t.Fatal(err)
	}
	for _, continuation := range []string{"attach", "image paste", "host alerts set"} {
		_, release, err := c.beginGrant(ctx, continuation, false)
		if err != nil {
			t.Fatalf("metadata continuation %q became a new value family: %v", continuation, err)
		}
		release()
	}
	if err := allowRequestGrant(ctx, c.Host, c.HostID(), TierOwn, Outbound{Kind: OutboundPaste}, LoadSettings()); err != nil {
		t.Fatal(err)
	}
	if requestGrantOperation(r) != grantOpNew {
		t.Fatal("a continuation changed the selected family")
	}
}

func TestLegacyResumePinsTargetNamesAndCannotUpgradeApprovalIntoGroup(t *testing.T) {
	_, parent, c, r := sharedGrantFixture(t)
	c.Features = []string{"resume", "secrets"}
	ctx := WithGrantRequest(t.Context(), r)
	if err := c.CheckResumeGrants(ctx, parent.SessionID, []string{"API_KEY"}); err != nil {
		t.Fatal(err)
	}
	requireScopeRefusal(t, c.CheckResumeGrants(ctx, "uvw234xy", []string{"API_KEY"}))
	requireScopeRefusal(t, c.CheckResumeGrants(ctx, parent.SessionID, []string{"API_KEY", "OTHER_KEY"}))
	c.Features = append(c.Features, "spawn-v1", "sandbox-snapshot-v1")
	if _, _, err := c.ResumeSecretTarget(ctx, parent.SessionID, []string{"API_KEY"}); err == nil {
		t.Fatal("legacy approval silently acquired a new membership snapshot")
	}
}

func TestConfirmationScopeIncludesValueOperation(t *testing.T) {
	h := h5GrantState(t)
	newRequest := NewGrantRequest(false, false)
	newContext := WithGrantRequest(t.Context(), newRequest)
	if err := requireGrantOperation(newContext, grantOpNew); err != nil {
		t.Fatal(err)
	}
	settings := Settings{SecretTiers: map[string]string{"API_KEY": "shared"}}
	grant := Outbound{Kind: OutboundSecret, Name: "API_KEY"}
	err := allowRequestGrant(newContext, h, h5Policy().HostID, TierShared, grant, settings)
	var question *ConfirmError
	if !errors.As(err, &question) {
		t.Fatal(err)
	}
	if _, err := ConfirmGrant(newContext, err); err != nil {
		t.Fatal(err)
	}
	resume := NewGrantRequest(false, false)
	resume.confirmed = maps.Clone(newRequest.confirmed)
	resumeContext := WithGrantRequest(t.Context(), resume)
	if err := requireGrantOperation(resumeContext, grantOpResume); err != nil {
		t.Fatal(err)
	}
	if err := allowRequestGrant(resumeContext, h, h5Policy().HostID, TierShared, grant, settings); !errors.As(err, &question) {
		t.Fatal("a copied New answer authorised Resume")
	}
}

func TestRefusedJoinCannotDonateItsRequestToAnUnwalledNew(t *testing.T) {
	_, parent, c, r := sharedGrantFixture(t)
	c.Features = []string{"resume", "secrets"}
	ctx := WithGrantRequest(t.Context(), r)
	if err := c.CheckJoinGrants(ctx, parent.SessionID, nil); err == nil {
		t.Fatal("old helper unexpectedly supported Join")
	}
	requireScopeRefusal(t, c.CheckNewGrants(ctx, nil, false))
	if err := c.CheckNewGrants(WithGrantRequest(t.Context(), NewGrantRequest(false, false)), nil, false); err != nil {
		t.Fatalf("a separate explicit New lost legacy compatibility: %v", err)
	}
}
