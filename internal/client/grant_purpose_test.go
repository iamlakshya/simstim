package client

import (
	"errors"
	"maps"
	"strings"
	"testing"
)

func TestLocalReviewConfirmationCannotBecomeRemoteApproval(t *testing.T) {
	h := h5GrantState(t)
	review := &GrantRequest{purpose: grantLocalReview}
	ctx := WithGrantRequest(t.Context(), review)
	settings := Settings{SecretTiers: map[string]string{"API_KEY": "shared"}}
	grant := Outbound{Kind: OutboundSecret, Name: "API_KEY"}
	err := allowRequestGrant(ctx, h, h5Policy().HostID, TierShared, grant, settings)
	var q *ConfirmError
	if !errors.As(err, &q) || !strings.Contains(q.Prompt, "on this computer to review pulled files") ||
		strings.Contains(strings.ToLower(q.Prompt+q.Error()), "send") {
		t.Fatalf("local review used a remote-send question: %v", err)
	}
	ctx, err = ConfirmGrant(ctx, err)
	if err != nil {
		t.Fatal(err)
	}
	if err := allowRequestGrant(ctx, h, h5Policy().HostID, TierShared, grant, settings); err != nil {
		t.Fatalf("same-purpose confirmation was not retained: %v", err)
	}
	remote := NewGrantRequest(false, false)
	remote.confirmed = maps.Clone(review.confirmed)
	if err := allowRequestGrant(WithGrantRequest(t.Context(), remote), h, h5Policy().HostID, TierShared, grant, settings); !errors.As(err, &q) {
		t.Fatal("copied local-review answers authorised remote disclosure")
	}
}

func TestLocalReviewContextCannotPublishFetchOrRecordOutbound(t *testing.T) {
	h, parent, c, _ := sharedGrantFixture(t)
	ctx := WithGrantRequest(t.Context(), &GrantRequest{purpose: grantLocalReview})
	if _, _, err := c.beginGrant(ctx, "new", false); err == nil {
		t.Fatal("a local review could begin a remote publication")
	}
	if _, err := FetchHostSecrets(ctx, h, nil, false); err == nil {
		t.Fatal("a local review used host-wide secret fetching")
	}
	if _, err := FetchSessionSecrets(ctx, h, parent.SessionID, nil, false); err == nil {
		t.Fatal("a local review used the remote producer's fetch path")
	}
	if err := c.CheckJoinGrants(ctx, parent.SessionID, nil); err == nil {
		t.Fatal("a local review prepared a member producer")
	}
	if _, _, err := c.ResumeSecretTarget(ctx, parent.SessionID, nil); err == nil {
		t.Fatal("a local review prepared a resume bundle")
	}
	if err := recordRequestGrant(ctx, h, parent.HostID, TierOwn, Outbound{Kind: OutboundSecret, Name: "API_KEY"}); err == nil {
		t.Fatal("a local review wrote an outbound exposure")
	}
	if rows, err := GrantsForHostContext(t.Context(), h, parent.HostID); err != nil || len(rows) != 0 {
		t.Fatalf("local review changed outbound history: %+v %v", rows, err)
	}
}

func TestUnknownPurposeAndMismatchedQuestionFailClosed(t *testing.T) {
	h := h5GrantState(t)
	r := &GrantRequest{purpose: grantPurpose(255)}
	ctx := WithGrantRequest(t.Context(), r)
	if err := allowRequestGrant(ctx, h, h5Policy().HostID, TierOwn, Outbound{Kind: OutboundSecret, Name: "API_KEY"}, Settings{}); err == nil {
		t.Fatal("an open-ended purpose was accepted")
	}
	r.purpose = grantRemote
	q := &scopedGrantConfirmation{request: r, scope: grantScope{purpose: grantLocalReview}, ConfirmError: &ConfirmError{}}
	if _, err := ConfirmGrant(ctx, q); err == nil {
		t.Fatal("a different-purpose question donated an approval")
	}
}
