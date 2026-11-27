package client

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func isolateTypedSecretSettings(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
		t.Setenv(name, t.TempDir())
	}
	if err := os.MkdirAll(ConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}
}

func secretSettingsMarkerCommand(marker string) string {
	if runtime.GOOS == "windows" {
		return "echo executed > \"" + marker + "\""
	}
	return "printf executed > " + ShellWord(marker)
}

func requireSecretSettingsMarkerAbsent(t *testing.T, marker string) {
	t.Helper()
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("metadata validation executed a configured command")
	}
}

func TestTypedSettingsDecodeLegacyAndStructuredDefinitionsThroughTheSameReader(t *testing.T) {
	isolateTypedSecretSettings(t)
	text := "[secrets]\nToken = ' printf legacy '\n" +
		"[secrets.TOKEN]\nrun = 'printf table'\nhosts = ['API.EXAMPLE.COM']\nheader = 'authorization'\nformat = 'Bearer {value}'\non_hosts = []\n"
	if err := os.WriteFile(settingsPath(), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, warning := ReadSettings()
	if warning != "" || settings.Secrets["Token"].Run != " printf legacy " || settings.Secrets["Token"].OnHosts != nil {
		t.Fatal("legacy settings changed their free-map name, command or absent restriction")
	}
	view, err := NormaliseSettingsSecrets(settings)
	if err != nil || len(view.Commands) != 2 || view.Commands["Token"] != "printf legacy" || view.Commands["TOKEN"] != "printf table" {
		t.Fatal("actual settings decoder and normaliser disagree on the strict union")
	}
	if view.Binds["TOKEN"].Header != "Authorization" || !reflect.DeepEqual(view.Binds["TOKEN"].Hosts, []string{"api.example.com:443"}) {
		t.Fatal("structured settings bypassed canonical binding validation")
	}
	if restriction, present := view.OnHosts["TOKEN"]; !present || restriction == nil || len(restriction) != 0 {
		t.Fatal("explicit empty on_hosts was lost between settings and H5's view")
	}
	if _, present := view.OnHosts["Token"]; present {
		t.Fatal("legacy missing on_hosts became an explicit restriction")
	}
	if err := ValidateSettings(settings); err != nil {
		t.Fatal(err)
	}
}

func TestConfiguredSecretNamesReturnsTheWholeOwnedNameSetWithoutExecution(t *testing.T) {
	isolateTypedSecretSettings(t)
	marker := filepath.Join(t.TempDir(), "must-not-execute")
	command := secretSettingsMarkerCommand(marker)
	settings := Settings{Secrets: map[string]SecretDefinition{
		"Token": {Run: command}, "TOKEN": {Run: command, OnHosts: []string{}},
		"SCOPED_KEY":     {Run: command, OnHosts: []string{"Box", "box"}},
		"DEPLOY_KEY":     {Run: command, Kind: "ssh-key"},
		"OPENAI_API_KEY": {Run: command},
	}, SecretTiers: map[string]string{"TOKEN": "shared", "SCOPED_KEY": "own"}}
	names, err := ConfiguredSecretNames(settings)
	want := map[string]bool{"Token": true, "TOKEN": true, "SCOPED_KEY": true, "DEPLOY_KEY": true, "OPENAI_API_KEY": true}
	if err != nil || !reflect.DeepEqual(names, want) {
		t.Fatal("name-only API filtered on_hosts, tier or kind, or invented an agent-default name")
	}
	requireSecretSettingsMarkerAbsent(t, marker)
	delete(names, "TOKEN")
	names["EXTRA"] = true
	next, err := ConfiguredSecretNames(settings)
	if err != nil || !reflect.DeepEqual(next, want) {
		t.Fatal("name-only result retained a mutable name cache or caller map")
	}
	view, err := NormaliseSettingsSecrets(settings)
	if err != nil || view.Binds["OPENAI_API_KEY"].Header != "Authorization" || view.Kinds["DEPLOY_KEY"] != "ssh-key" {
		t.Fatal("whole-settings view lost canonical global defaults or SSH key kind")
	}
	view.OnHosts["SCOPED_KEY"][0] = "changed"
	view.Commands["Token"] = "changed"
	if settings.Secrets["SCOPED_KEY"].OnHosts[0] != "Box" || settings.Secrets["Token"].Run != command {
		t.Fatal("normalised view retained mutable settings state")
	}
	requireSecretSettingsMarkerAbsent(t, marker)
}

func TestTypedSettingsValidateEveryUnusedDefinitionBeforeAnyCommand(t *testing.T) {
	for _, invalid := range []SecretDefinition{
		{}, {Run: "printf unused", Kind: "unknown"},
		{Run: "printf unused", OnHosts: []string{" invalid "}},
		{Run: "printf unused", Hosts: []string{}},
		{Run: "printf unused", Hosts: []string{"example.com:80"}, Header: "Authorization", Format: "Bearer {value}"},
	} {
		t.Run(invalid.Kind+invalid.Header+strings.Join(invalid.OnHosts, "_"), func(t *testing.T) {
			isolateTypedSecretSettings(t)
			marker := filepath.Join(t.TempDir(), "must-not-execute")
			settings := Settings{Secrets: map[string]SecretDefinition{"WANTED": {Run: secretSettingsMarkerCommand(marker)}, "UNUSED": invalid}}
			view, err := NormaliseSettingsSecrets(settings)
			if err == nil || !reflect.DeepEqual(view, SecretConfigView{}) {
				t.Fatal("invalid unused definition published partial runnable commands")
			}
			if names, err := ConfiguredSecretNames(settings); err == nil || names != nil {
				t.Fatal("name-only API skipped an invalid unused definition")
			}
			for _, names := range [][]string{{"WANTED"}, nil} {
				if values, err := FetchSecrets(t.Context(), settings, names, false); err == nil || values != nil {
					t.Fatal("fetch skipped complete settings validation")
				}
			}
			if err := Allow(Host{Name: "Box"}, TierOwn, Outbound{Kind: OutboundSSHAgent}, settings); err == nil {
				t.Fatal("an unrelated outbound grant bypassed invalid secret metadata")
			}
			requireSecretSettingsMarkerAbsent(t, marker)
		})
	}
}

func TestTypedSettingsPreserveReadAndUnrelatedTierBarriers(t *testing.T) {
	isolateTypedSecretSettings(t)
	marker := filepath.Join(t.TempDir(), "must-not-execute")
	settings := Settings{Secrets: map[string]SecretDefinition{"WANTED": {Run: secretSettingsMarkerCommand(marker)}}, SecretTiers: map[string]string{"UNUSED": "invalid"}}
	if _, err := NormaliseSettingsSecrets(settings); err == nil {
		t.Fatal("typed secret migration skipped unrelated tier validation")
	}
	readFailure := errors.New("owned read failure")
	settings.loadErr = readFailure
	if _, err := NormaliseSettingsSecrets(settings); err != readFailure {
		t.Fatal("normalisation erased the original load error")
	}
	if _, err := ConfiguredSecretNames(settings); err != readFailure {
		t.Fatal("name-only API reset the security read barrier")
	}
	if _, err := FetchSecrets(t.Context(), settings, []string{"WANTED"}, false); err != readFailure {
		t.Fatal("fetch reset the security read barrier")
	}
	requireSecretSettingsMarkerAbsent(t, marker)
}

func TestMalformedTypedSecretSettingsWarnWithoutRestoringPermissiveAuthority(t *testing.T) {
	for index, body := range []string{
		"[secrets.UNUSED]\nrun = 'printf command-marker'\nvalue = 'hidden-marker'\n",
		"[secrets.UNUSED]\nrun = 'printf command-marker'\nRUN = 'printf other-marker'\n",
		"[secrets.UNUSED]\nrun = 'printf command-marker'\nhosts = 'example.com'\n",
		"[secrets.UNUSED]\nrun = ['printf command-marker']\n",
		"[secrets]\nUNUSED = true\n",
	} {
		t.Run(strconv.Itoa(index), func(t *testing.T) {
			isolateTypedSecretSettings(t)
			text := "[ui]\nmotion = 'off'\n" + body
			if err := os.WriteFile(settingsPath(), []byte(text), 0o600); err != nil {
				t.Fatal(err)
			}
			settings, warning := ReadSettings()
			if warning == "" || settings.UI.Motion != "auto" || settings.Secrets != nil || settings.SecretTiers != nil {
				t.Fatal("malformed union retained partial settings or suppressed its ordinary warning")
			}
			if err := ValidateSettings(settings); err == nil {
				t.Fatal("warning/default fallback erased the global security barrier")
			} else if strings.Contains(err.Error(), "command-marker") || strings.Contains(err.Error(), "hidden-marker") || strings.Contains(warning, "command-marker") || strings.Contains(warning, "hidden-marker") {
				t.Fatal("ordinary warning or security barrier exposed a command or value")
			}
			if names, err := ConfiguredSecretNames(settings); err == nil || names != nil {
				t.Fatal("warning fallback invented a valid empty secret name set")
			}
			if _, err := ReadPolicySettings(); err == nil {
				t.Fatal("strict policy read bypassed the same malformed configuration")
			}
			if check := fileCheck("config.toml", settingsPath(), &Settings{}); check.OK {
				t.Fatal("doctor's strict checker accepted malformed secret metadata")
			}
		})
	}
}

func TestTypedSettingsRejectUnusedReservedSocketDefinition(t *testing.T) {
	isolateTypedSecretSettings(t)
	settings := Settings{Secrets: map[string]SecretDefinition{"SSH_AUTH_SOCK": {Run: "printf forbidden"}}}
	if _, err := NormaliseSettingsSecrets(settings); err == nil {
		t.Fatal("unused reserved socket entered the canonical view")
	}
	if _, err := ConfiguredSecretNames(settings); err == nil {
		t.Fatal("unused reserved socket was reported as a usable configured credential")
	}
}
