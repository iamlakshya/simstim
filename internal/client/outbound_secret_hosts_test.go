package client

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestSecretOnHostsPresenceAndOriginalSavedNameApplyToBothCredentialKinds(t *testing.T) {
	for _, kind := range []OutboundKind{OutboundSecret, OutboundSSHKey} {
		for _, tc := range []struct {
			name    string
			host    Host
			onHosts []string
			allowed bool
		}{
			{"absent", Host{Name: "box"}, nil, true},
			{"present-empty", Host{Name: "box"}, []string{}, false},
			{"exact-label", Host{Name: "box", SSH: "generated-target"}, []string{"box"}, true},
			{"same-SSH-other-label", Host{Name: "alias", SSH: "generated-target"}, []string{"box"}, false},
			{"case-is-exact", Host{Name: "Box"}, []string{"box"}, false},
		} {
			t.Run(string(kind)+"/"+tc.name, func(t *testing.T) {
				settings := Settings{Secrets: map[string]SecretDefinition{"TOKEN": {Run: "printf generated-fixture", OnHosts: tc.onHosts}}}
				err := Allow(tc.host, TierOwn, Outbound{Kind: kind, Name: "TOKEN"}, settings)
				if tc.allowed {
					if err != nil {
						t.Fatal(err)
					}
					return
				}
				var refused *UserError
				if !errors.As(err, &refused) || refused.Code != "secret_not_here" || refused.Kind != KindRefused || refused.Hint == "" {
					t.Fatalf("the exact saved-label restriction did not refuse with its hint: %v", err)
				}
			})
		}
	}
}

func TestSecretOnHostsCannotRaiseTierOrAskForAnIneligibleHost(t *testing.T) {
	settings := Settings{Secrets: map[string]SecretDefinition{"TOKEN": {Run: "printf generated", OnHosts: []string{"box"}}}}
	h := Host{Name: "box"}
	if err := Allow(h, TierShared, Outbound{Kind: OutboundSecret, Name: "TOKEN", Confirmed: true}, settings); err == nil {
		t.Fatal("an on_hosts match raised the secret's minimum tier")
	}
	settings.SecretTiers = map[string]string{"TOKEN": "shared"}
	settings.Secrets["TOKEN"] = SecretDefinition{Run: "printf generated", OnHosts: []string{}}
	err := Allow(h, TierShared, Outbound{Kind: OutboundSecret, Name: "TOKEN"}, settings)
	var question *ConfirmError
	var refused *UserError
	if errors.As(err, &question) || !errors.As(err, &refused) || refused.Code != "secret_not_here" {
		t.Fatalf("a denied host reached the terminal approval path: %v", err)
	}
}

func TestSecretOnHostsUsesWholeSettingsBarrierForUnusedDefinitions(t *testing.T) {
	settings := Settings{Secrets: map[string]SecretDefinition{
		"TOKEN":  {Run: "printf generated", OnHosts: []string{"box"}},
		"UNUSED": {Run: "printf unused", OnHosts: []string{"invalid/host"}},
	}}
	var invalid *UserError
	if err := Allow(Host{Name: "box"}, TierOwn, Outbound{Kind: OutboundSecret, Name: "TOKEN"}, settings); !errors.As(err, &invalid) || invalid.Code != "bad_config" {
		t.Fatalf("an unused invalid definition escaped the shared whole-settings barrier: %v", err)
	}
}

func TestSecretOnHostsRefusesBeforeActualCommandAndFinalFrame(t *testing.T) {
	h := h5GrantState(t)
	marker := filepath.Join(t.TempDir(), "forbidden-value-command")
	command := "touch " + ShellWord(marker) + "; printf generated-not-for-this-host"
	config := fmt.Sprintf("[secrets.API_KEY]\nrun = %q\non_hosts = [\"another\"]\n", command)
	if err := os.WriteFile(settingsPath(), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := WithGrantRequest(t.Context(), NewGrantRequest(false, false))
	var refused *UserError
	if _, err := FetchHostSecrets(ctx, h, []string{"API_KEY"}, false); !errors.As(err, &refused) || refused.Code != "secret_not_here" {
		t.Fatalf("prefetch did not apply the saved-host restriction: %v", err)
	}
	writes := 0
	c := replyingCreationConn(t, h, []string{"session.id", "secrets"}, func(NewArgs) { writes++ })
	if _, err := c.NewSession(ctx, NewArgs{Secrets: map[string]api.Secret{"API_KEY": "generated-direct-value"}}); !errors.As(err, &refused) || refused.Code != "secret_not_here" || writes != 0 {
		t.Fatalf("final publication bypassed on_hosts: writes=%d error=%v", writes, err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("an on_hosts denial ran the configured command")
	}
}
