package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func policyConfigHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
		t.Setenv(name, filepath.Join(home, name))
	}
	if err := os.MkdirAll(ConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestProfileAfterNamedRepositoryAndSessionLists(t *testing.T) {
	settings := DefaultSettings()
	settings.Sandbox.Secrets = []string{"ALL"}
	settings.Repos = map[string]RepoSettings{
		"forge.example/team/*":   {LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Secrets: []string{"ORG"}}}},
		"forge.example/team/app": {LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Profile: policyString("review"), Secrets: []string{"REPO"}}, Network: api.NetworkValues{Allow: []string{"api.example.com"}}}},
	}
	settings.Profiles = map[string]api.LayerValues{"review": {Sandbox: api.SandboxValues{Secrets: []string{"!ALL", "PROFILE"}}, Network: api.NetworkValues{Deny: []string{"api.example.com"}}}}
	layers, err := ResolvePolicyLayers(settings, Host{Name: "box"}, "git@forge.example:team/app.git", "", api.LayerValues{Sandbox: api.SandboxValues{Secrets: []string{"SESSION"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(layers) != 6 || layers[2].Name != "forge.example/team/*" || layers[3].Name != "forge.example/team/app" || layers[4].Name != "review" {
		t.Fatalf("layers=%+v", layers)
	}
}

func TestStrictNamedLayerAliasesAndSessionSet(t *testing.T) {
	policyConfigHome(t)
	if err := os.WriteFile(settingsPath(), []byte("[profiles.review.network]\nunknown='deny'\nUNKNOWN='allow'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPolicySettings(); err == nil {
		t.Fatal("accepted alias collision in a named profile")
	}
	values, err := ParseLayerSets([]string{"network.unknown=deny", "sandbox.docker=false", "sandbox.cpus=0", "sandbox.secrets=['A','!B']"})
	if err != nil {
		t.Fatal(err)
	}
	if values.Network.Unknown == nil || *values.Network.Unknown != "deny" || values.Sandbox.Docker == nil || *values.Sandbox.Docker || values.Sandbox.CPUs == nil || *values.Sandbox.CPUs != 0 {
		t.Fatalf("lost explicit values: %+v", values)
	}
}

func policyString(value string) *string { return &value }

func TestConfiguredWallRaisesWithNoteAndAutoKeepsFloor(t *testing.T) {
	layers := []api.Layer{{Kind: api.LayerAll, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Wall: policyString("process")}}}, {Kind: api.LayerSession, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Wall: policyString("auto")}}}}
	if sessionHasWall(layers) {
		t.Fatal("auto became an explicit named downgrade")
	}
	h := Host{Name: "box", WallFloor: "microvm"}
	if raisedConfigWall("process", h.WallFloor) != "microvm" || !strings.Contains(PolicyWallNote(h, layers), "using microvm") {
		t.Fatal("configured wall below floor was silent")
	}
	layers[1].Sandbox.Wall = policyString("process")
	if !sessionHasWall(layers) || PolicyWallNote(h, layers) != "" {
		t.Fatal("explicit wall became a silent config raise")
	}
}
