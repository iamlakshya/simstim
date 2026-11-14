package client

import (
	"os"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestTrustedRepositoryConfigKeepsConfiguredPullCeiling(t *testing.T) {
	policyConfigHome(t)
	config := "[repos.\"example.org/team/project\".sandbox]\npull_max='1G'\n[profiles.work.sandbox]\npull_max='768M'\n"
	if err := os.WriteFile(settingsPath(), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := ReadPolicySettings()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		profile string
		want    int64
	}{{"", 1 << 30}, {"work", 768 << 20}} {
		layers, err := ResolvePolicyLayers(settings, Host{Name: "box"}, "example.org/team/project", test.profile, api.LayerValues{})
		if err != nil {
			t.Fatal(err)
		}
		a := NewArgs{Layers: layers, RepositoryPolicy: &api.RepositoryPolicy{Layer: api.Layer{
			Kind: api.LayerRepo, Name: "example.org/team/project", Source: api.SourceRepositoryFile,
			LayerValues: api.LayerValues{Network: api.NetworkValues{Deny: []string{"blocked.example.com"}}},
		}}}
		raw, err := encodeLocalPolicySources(a)
		if err != nil {
			t.Fatal(err)
		}
		pin, err := api.DecodePolicySourcePin(raw)
		if err != nil {
			t.Fatal(err)
		}
		limit, err := pullMaxFromPin(pin)
		if err != nil || limit != test.want {
			t.Fatalf("configured profile %q pull ceiling = %d, %v; want %d", test.profile, limit, err, test.want)
		}
		for _, ref := range pin.Layers {
			if ref.Source == api.SourceRepositoryFile {
				t.Fatal("repository file replaced a trusted configured source")
			}
		}
	}
}

func TestCheckedRepositoryWallAndNetworkKeepLocalPullCeiling(t *testing.T) {
	wall := "process"
	a := NewArgs{Layers: []api.Layer{pullLayer(api.LayerProfile, "work", "1G"), {Kind: api.LayerSession}}, RepositoryPolicy: &api.RepositoryPolicy{Layer: api.Layer{Kind: api.LayerRepo, Name: "example.org/team/project", Source: api.SourceRepositoryFile, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Wall: &wall}, Network: api.NetworkValues{Deny: []string{"blocked.example.com"}}}}}}
	raw, err := encodeLocalPolicySources(a)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := api.DecodePolicySourcePin(raw)
	if err != nil || pin == nil || pin.Resources == nil || pin.Resources.PullMax != 1<<30 {
		t.Fatalf("checked repository changed local ceiling: %+v, %v", pin, err)
	}
	if pin.Session.Sandbox.Wall != nil {
		t.Fatal("repository file became an original local session setting")
	}
	for _, ref := range pin.Layers {
		if ref.Source == api.SourceRepositoryFile {
			t.Fatal("checked repository file became a local source identity")
		}
	}
}
