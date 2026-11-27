package client

import (
	"errors"
	"testing"
)

func TestOutboundGrantMatrix(t *testing.T) {
	h := Host{Name: "box", Repos: []string{"example.com/team/*"}}
	rows := []struct {
		kind                   OutboundKind
		name                   string
		own, throwaway, shared bool
	}{
		{OutboundSecret, "API_KEY", true, false, false},
		{OutboundSSHKey, "DEPLOY_KEY", true, false, false},
		{OutboundSSHAgent, "", true, false, false},
		{OutboundAlertToken, "ntfy", true, false, false},
		{OutboundRepo, "example.com/team/app", true, true, true},
		{OutboundForward, "", true, true, true},
		{OutboundPaste, "", true, true, true},
		{OutboundReverseForward, "", true, false, false},
		{OutboundPool, "", true, false, false},
		{OutboundDefaultHost, "", true, true, false},
		{OutboundRootRun, "", true, true, false},
		{OutboundSilentUpgrade, "", true, true, false},
		{OutboundNet, "example.com:443", true, true, true},
		{OutboundPrivateNet, "", true, false, false},
		{OutboundFloor, "process", true, true, false},
		{OutboundTemplate, "", true, true, false},
		{OutboundTemplate, "chosen", true, true, true},
	}
	for _, row := range rows {
		for i, tier := range []Tier{TierOwn, TierThrowaway, TierShared} {
			want := []bool{row.own, row.throwaway, row.shared}[i]
			err := Allow(h, tier, Outbound{Kind: row.kind, Name: row.name}, DefaultSettings())
			if (err == nil) != want {
				t.Errorf("%s/%q on %s: %v, want allowed=%v", row.kind, row.name, tier, err, want)
			}
		}
	}
	settings := DefaultSettings()
	settings.SecretTiers = map[string]string{"API_KEY": "shared", "DEPLOY_KEY": "throwaway"}
	if err := Allow(h, TierThrowaway, Outbound{Kind: OutboundSecret, Name: "API_KEY"}, settings); err != nil {
		t.Fatal(err)
	}
	var question *ConfirmError
	if err := Allow(h, TierShared, Outbound{Kind: OutboundSecret, Name: "API_KEY"}, settings); !errors.As(err, &question) {
		t.Fatalf("a shared credential bypassed its terminal question: %v", err)
	}
	if err := Allow(h, TierShared, Outbound{Kind: OutboundSecret, Name: "API_KEY", Confirmed: true}, settings); err != nil {
		t.Fatal(err)
	}
	if err := Allow(h, TierShared, Outbound{Kind: OutboundSSHKey, Name: "DEPLOY_KEY", Confirmed: true}, settings); err == nil {
		t.Fatal("a terminal answer raised a throwaway-only key's tier")
	}
	if err := Allow(h, TierOwn, Outbound{Kind: OutboundFloor, Name: "vz"}, settings); err == nil {
		t.Fatal("an unrecognised wall level became a valid floor")
	}
}

func TestPublicNetGrantDoesNotAuthorizePrivateClassificationOrTrackMetadata(t *testing.T) {
	h := Host{Name: "box"}
	if !OutboundNet.Valid() || TrackedGrantKind(OutboundNet) {
		t.Fatal("public net must be a valid untracked policy grant")
	}
	for _, tier := range []Tier{TierOwn, TierThrowaway, TierShared} {
		for _, name := range []string{"", " ", "example.com:443\n"} {
			if err := Allow(h, tier, Outbound{Kind: OutboundNet, Name: name}, DefaultSettings()); err == nil {
				t.Fatalf("accepted an absent or malformed net name %q on %s", name, tier)
			}
		}
		if tier != TierOwn {
			for _, confirmed := range []bool{false, true} {
				if err := Allow(h, tier, Outbound{Kind: OutboundPrivateNet, Name: "127.0.0.1:443", Confirmed: confirmed}, DefaultSettings()); err == nil {
					t.Fatal("public-net support or a confirmation authorized a classified private destination")
				}
			}
		}
	}
}
