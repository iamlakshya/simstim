package client

import (
	"errors"
	"os"
	"runtime"
	"testing"
)

func TestBoundedSettingsKeepTheGlobalAgentGrantBarrier(t *testing.T) {
	for _, row := range []struct {
		name, text string
		warning    bool
	}{
		{"invalid-unrelated-tier", "[secret_tiers]\nUNRELATED = \"borrowed\"\n", false},
		{"wrong-secret-type", "[ui]\nmotion = \"off\"\n[secrets]\nUNRELATED = 17\n", true},
		{"schema-alias", "[SECRET_TIERS]\nA = \"own\"\n[secret_tiers]\nB = \"shared\"\n", true},
	} {
		t.Run(row.name, func(t *testing.T) {
			isolateH5GrantHome(t)
			if err := os.MkdirAll(ConfigDir(), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(settingsPath(), []byte(row.text), 0600); err != nil {
				t.Fatal(err)
			}
			settings, warning := ReadSettings()
			if (warning != "") != row.warning {
				t.Fatalf("warning=%q", warning)
			}
			if row.warning && (settings.UI.Motion != "auto" || settings.Secrets != nil || settings.SecretTiers != nil) {
				t.Fatal("a failed decode retained partial settings")
			}
			var refusal *UserError
			err := Allow(Host{Name: "box", SSH: "box.invalid"}, TierOwn, Outbound{Kind: OutboundSSHAgent}, settings)
			if !errors.As(err, &refusal) || refusal.Code != "bad_config" {
				t.Fatalf("an unrelated invalid configuration bypassed the global barrier: %v", err)
			}
		})
	}
}

func TestBoundedSettingsKeepFreeMapKeysAndDanglingLinkRefusal(t *testing.T) {
	isolateH5GrantHome(t)
	if err := os.MkdirAll(ConfigDir(), 0700); err != nil {
		t.Fatal(err)
	}
	text := "[secrets]\nToken = \"printf one\"\nTOKEN = \"printf two\"\n[secret_tiers]\nToken = \"own\"\nTOKEN = \"shared\"\n"
	if err := os.WriteFile(settingsPath(), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	settings, warning := ReadSettings()
	if warning != "" || len(settings.Secrets) != 2 || len(settings.SecretTiers) != 2 || settings.SecretTiers["Token"] != "own" || settings.SecretTiers["TOKEN"] != "shared" {
		t.Fatal("the shared schema inspector folded free-map values or keys")
	}
	if err := ValidateSettings(settings); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		t.Skip("the remaining branch requires a symlink privilege")
	}
	if err := os.Remove(settingsPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(settingsPath()+".missing", settingsPath()); err != nil {
		t.Fatal(err)
	}
	settings, warning = ReadSettings()
	if warning == "" || settings.Secrets != nil || settings.SecretTiers != nil {
		t.Fatal("a dangling link was treated as absent valid settings")
	}
	var refusal *UserError
	if err := ValidateSettings(settings); !errors.As(err, &refusal) || refusal.Code != "config_read" {
		t.Fatalf("dangling-link barrier: %v", err)
	}
}
