package client

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestLocalPurposeRequestsCannotBecomeSessionOrReviewAuthority(t *testing.T) {
	h := h5GrantState(t)
	c := finishGeneratedTransportFixture(t, h, h5Policy().HostID, "session.id", "secrets")
	for _, request := range []*GrantRequest{NewPushGrantRequest(), NewRootStepGrantRequest()} {
		ctx := WithGrantRequest(t.Context(), request)
		if _, _, err := c.beginGrant(ctx, "new", true); err == nil {
			t.Fatal("localpurpose acquired ordinary publication authority")
		}
		if _, err := FetchHostSecrets(ctx, h, nil, false); err == nil {
			t.Fatal("localpurpose selected session values")
		}
		if _, err := FetchReviewSecrets(ctx, h, h5Policy().SessionID, nil, nil, false); err == nil {
			t.Fatal("localpurpose became scanner review")
		}
	}
}

func TestPushConsentSealsCompleteLocalFlagIntentBeforeAnyVisibility(t *testing.T) {
	h := h5GrantState(t)
	c := finishGeneratedTransportFixture(t, h, h5Policy().HostID, "policy", api.FeaturePolicyPush)
	deadline := time.Date(time.Now().Year()+1, 1, 2, 3, 4, 5, 123456789, time.FixedZone("offset", 7200))
	args := api.PolicyAddArgs{ID: h5Policy().SessionID, Allow: true, Door: api.DoorPush, Target: "github.com/Owner/repo.git.git", Once: true, Until: deadline}
	intent, err := newPushGrantIntent(c, args, true)
	if err != nil {
		t.Fatal(err)
	}
	r := NewPushGrantRequest()
	ctx := WithGrantRequest(t.Context(), r)
	if err := bindRequestPushIntent(ctx, c, intent); err != nil {
		t.Fatal(err)
	}
	if intent.repo != "github.com/Owner/repo.git" || intent.mode != api.PushModePublic || !intent.until.Equal(deadline) || intent.until.Location() != time.UTC {
		t.Fatal("canonical local intent lost repo or fulldeadline")
	}
	if err := requireRequestPushIntent(ctx, c, intent, intent.args()); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*pushGrantIntent){func(p *pushGrantIntent) { p.once = false }, func(p *pushGrantIntent) { p.until = p.until.Add(time.Nanosecond) }, func(p *pushGrantIntent) { p.repo = "github.com/Owner/other" }, func(p *pushGrantIntent) { p.mode = "" }} {
		other := *intent
		change(&other)
		if err := bindRequestPushIntent(ctx, c, &other); err == nil {
			t.Fatal("changed push target/mode/time donated original consent")
		}
	}
	remote := NewGrantRequest(false, false)
	remote.confirmed = maps.Clone(r.confirmed)
	if err := bindRequestPushIntent(WithGrantRequest(t.Context(), remote), c, intent); err == nil {
		t.Fatal("network/remote request became localforge purpose")
	}
	if _, err := newPushGrantIntent(c, api.PolicyAddArgs{ID: args.ID, Allow: true, Door: api.DoorPush, Target: intent.repo, Mode: api.PushModePublic}, false); err == nil {
		t.Fatal("manifest mode manufactured local --public flag")
	}
}

func TestCanonicalPushAndRootLedgerKindsStayDistinct(t *testing.T) {
	for _, kind := range []OutboundKind{OutboundPush, OutboundPublicPush} {
		name, err := canonicalGrantName(kind, "github.com/Owner/repo.git")
		if err != nil || name != "github.com/Owner/repo.git" || !TrackedGrantKind(kind) || RotatableGrantKind(kind) {
			t.Fatal("newpurpose ledger identity lost exactsuffix orkind", kind, err)
		}
	}
	if _, err := canonicalGrantName(OutboundRootRun, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := canonicalGrantName(OutboundRootRun, ""); err == nil {
		t.Fatal("root ledger accepted a nameless attempt")
	}
}

func TestRootScriptHashAndRawDataCannotMintExecution(t *testing.T) {
	probe := api.CapProbe{OS: "linux", User: "fixture", UID: 1000}
	script, err := BuildRootScript(probe.User, probe.UID, []RootStepID{RootLinger}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := canonicalRootScript(probe, script); err != nil {
		t.Fatal(err)
	}
	changed := cloneRootScript(script)
	changed.Text += "\nprintf 'arbitrary'\n"
	hash := sha256.Sum256([]byte(changed.Text))
	changed.SHA256 = hex.EncodeToString(hash[:])
	if _, err := canonicalRootScript(probe, changed); err == nil {
		t.Fatal("correct raw hash bypassed fixed script builder")
	}
	r := NewRootStepGrantRequest()
	ctx := WithGrantRequest(t.Context(), r)
	if err := (&Conn{}).RunRootStep(ctx, script, RootRun, RootRunOptions{Passwordless: true}); err == nil {
		t.Fatal("raw script plusrootrequest constructed privileged execution")
	}
	if _, err := (&Conn{}).WriteRootScript(ctx, script); err == nil {
		t.Fatal("raw script plus root request obtained a review writer without the original probe/UID")
	}
}

func TestForgeVisibilityRequiresExactCompleteMetadata(t *testing.T) {
	request, err := pushForgeRequestFor("github.com/Owner/repository")
	if err != nil {
		t.Fatal(err)
	}
	view, err := decodePushForgeVisibility(request, strings.NewReader(`{"full_name":"owner/REPOSITORY","private":false}`))
	if err != nil || !view.public || view.repo != request.repo {
		t.Fatal("exactGitHub metadata failed", err)
	}
	for _, body := range []string{`{"full_name":"other/repository","private":false}`, `{"full_name":"Owner/repository"}`, `{"full_name":"Owner/repository","private":null}`, `{"full_name":"Owner/repository","private":false,"Private":false}`, `{"full_name":"Owner/repository","private":false} {}`, `{"full_name":"Owner/repository","private":false`} {
		if _, err := decodePushForgeVisibility(request, strings.NewReader(body)); err == nil {
			t.Fatal("incomplete/aliased/foreign metadata established visibility")
		}
	}
	reader := &forgeResponseReader{source: strings.NewReader("metadata-more"), remaining: 8}
	if _, err := io.ReadAll(reader); err == nil || errors.Is(err, io.EOF) {
		t.Fatal("wirebound became a successful artificial EOF", err)
	}
}
