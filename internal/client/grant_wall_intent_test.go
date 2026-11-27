package client

import (
	"context"
	"errors"
	"testing"
)

func TestWallGrantIntentCopiesNamesAndRequiresResolvedWall(t *testing.T) {
	for _, level := range []string{"", "auto", "PROCESS", "unknown"} {
		if _, err := NewWallGrantRequest(false, false, level, nil); err == nil {
			t.Fatalf("accepted unresolved wall %q", level)
		}
	}
	for _, level := range []string{"process", "gvisor", "microvm"} {
		if _, err := NewWallGrantRequest(false, true, level, nil); err == nil {
			t.Fatalf("accepted full agent intent for %s", level)
		}
	}
	for _, name := range []string{"", "bad-name", "NAME\n", "SSH_AUTH_SOCK"} {
		if _, err := NewWallGrantRequest(false, false, "process", []string{name}); err == nil {
			t.Fatalf("accepted invalid or reserved expose name %q", name)
		}
	}
	names := []string{"API_KEY"}
	r, err := NewWallGrantRequest(true, false, "process", names)
	if err != nil {
		t.Fatal(err)
	}
	names[0] = "OTHER_KEY"
	ctx := WithGrantRequest(t.Context(), r)
	if !r.Untrusted || requestCeiling(ctx, TierOwn) != TierThrowaway {
		t.Fatal("wall intent lost its lower request ceiling")
	}
	if err := allowWallSecretIntent(ctx, DefaultSettings(), "API_KEY"); err != nil {
		t.Fatal(err)
	}
	if err := allowWallSecretIntent(ctx, DefaultSettings(), "OTHER_KEY"); err == nil {
		t.Fatal("mutating the caller's names created another exposure answer")
	}
	fresh := WithGrantRequest(context.WithoutCancel(ctx), r)
	if grantRequestOf(fresh) != r || allowWallSecretIntent(fresh, DefaultSettings(), "API_KEY") != nil {
		t.Fatal("rebinding the same logical request lost exposure intent")
	}
}

func TestProcessExposureSupplementsHostTierAndSecretMarking(t *testing.T) {
	h := Host{Name: "box"}
	settings := DefaultSettings()
	settings.SecretTiers = map[string]string{"SCOPED_KEY": "throwaway"}
	r, err := NewWallGrantRequest(false, false, "process", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithGrantRequest(t.Context(), r)
	if err := allowSecretNames(ctx, h, "", TierOwn, settings, []string{"API_KEY"}); err == nil {
		t.Fatal("an own host silently exposed an own-tier secret into a process wall")
	}
	if err := allowSecretNames(ctx, h, "", TierOwn, settings, []string{"SCOPED_KEY"}); err != nil {
		t.Fatalf("a scoped throwaway secret was refused by the additional own-secret rule: %v", err)
	}
	exposed, err := NewWallGrantRequest(false, false, "process", []string{"API_KEY"})
	if err != nil {
		t.Fatal(err)
	}
	ctx = WithGrantRequest(t.Context(), exposed)
	if err := allowSecretNames(ctx, h, "", TierOwn, settings, []string{"API_KEY"}); err != nil {
		t.Fatal(err)
	}
	for _, tier := range []Tier{TierThrowaway, TierShared} {
		if err := allowSecretNames(ctx, h, "", tier, settings, []string{"API_KEY"}); err == nil {
			t.Fatalf("--expose raised the secret's host tier on %s", tier)
		}
	}
	settings.SecretTiers["API_KEY"] = "shared"
	var question *ConfirmError
	if err := allowSecretNames(ctx, h, "", TierShared, settings, []string{"API_KEY"}); !errors.As(err, &question) {
		t.Fatalf("--expose replaced the shared-host terminal answer: %v", err)
	}
}

func TestOutgoingWallMustMatchLocalIntentBeforeAnyPublication(t *testing.T) {
	r, err := NewWallGrantRequest(false, false, "process", []string{"API_KEY"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithGrantRequest(t.Context(), r)
	for _, actual := range []string{"", "none", "gvisor", "microvm", "auto"} {
		if err := checkWallGrantArgs(ctx, actual, false); err == nil {
			t.Fatalf("accepted actual wall %q for process intent", actual)
		}
	}
	if err := checkWallGrantArgs(ctx, "process", false); err != nil {
		t.Fatal(err)
	}
	if err := checkWallGrantArgs(ctx, "process", true); err == nil {
		t.Fatal("outgoing SSHAgent bypassed the constructor's process restriction")
	}
	// No connection, writer, config or policy exists. This must refuse at
	// the intent gate before any publication or saved-host lookup.
	var conn Conn
	if _, err := conn.NewSession(ctx, NewArgs{Wall: "none"}); err == nil {
		t.Fatal("a mismatched wall reached NewSession publication")
	}
	for _, actual := range []string{"", "none"} {
		legacy := WithGrantRequest(t.Context(), NewGrantRequest(false, false))
		if err := checkWallGrantArgs(legacy, actual, false); err != nil {
			t.Fatalf("legacy unwalled request changed: %v", err)
		}
	}
	if err := checkWallGrantArgs(t.Context(), "process", false); err == nil {
		t.Fatal("a wall without local constructor intent was accepted")
	}
}

func TestMissingCreationCannotBeReplacedByConstructorExposure(t *testing.T) {
	for _, level := range []string{"none", "process"} {
		r, err := NewWallGrantRequest(false, false, level, []string{"API_KEY"})
		if err != nil {
			t.Fatal(err)
		}
		ctx := WithGrantRequest(t.Context(), r)
		err = allowResumeSecretNames(ctx, Host{Name: "box"}, "", TierOwn, DefaultSettings(), []string{"API_KEY"}, nil)
		var refusal *UserError
		if !errors.As(err, &refusal) || refusal.Code != "secret_intent_missing" {
			t.Fatalf("a local %s constructor replaced a missing creation record: %v", level, err)
		}
	}
}

func TestUnsupportedResolvedWallRefusesBeforeAnyPrefetchStateRead(t *testing.T) {
	r, err := NewWallGrantRequest(false, false, "process", []string{"API_KEY"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithGrantRequest(t.Context(), r)
	var conn Conn
	if err := conn.CheckNewGrants(ctx, []string{"API_KEY"}, false); err == nil {
		t.Fatal("unsupported process wall passed prefetch admission")
	}
	if _, err := FetchHostSecrets(ctx, Host{Name: "box"}, []string{"API_KEY"}, false); err == nil {
		t.Fatal("unsupported process wall reached secret fetching")
	}
}
