package client

import (
	"slices"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

func TestHostRuleRepositoryLayersKeepBoundaryOrderAndAdditiveValues(t *testing.T) {
	settings := DefaultSettings()
	settings.Repos = map[string]RepoSettings{
		"forge.example/team/app":    {LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Secrets: []string{"EXACT"}}}},
		"forge.example/team/*":      {LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Secrets: []string{"ORG"}}}},
		"forge.example/*":           {LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Secrets: []string{"FORGE"}}}},
		"forge.example/team-labs/*": {LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Secrets: []string{"WRONG"}}}},
	}
	layers, err := matchingHostRuleRepositories(settings, "git@forge.example:team/app.git")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, layer := range layers {
		if layer.Kind != api.LayerRepo || layer.Source == api.SourceRepositoryFile {
			t.Fatal("a laptop section gained another layer identity")
		}
		names = append(names, layer.Name)
	}
	if !slices.Equal(names, []string{"forge.example/*", "forge.example/team/*", "forge.example/team/app"}) {
		t.Fatal("repository sections did not retain broadest-first order", names)
	}
	policy, _, err := egress.MergeLayers(layers)
	names = slices.Clone(policy.Sandbox.Secrets)
	slices.Sort(names)
	if err != nil || !slices.Equal(names, []string{"EXACT", "FORGE", "ORG"}) {
		t.Fatal("a narrower repository section erased broader secret names", policy.Sandbox.Secrets, err)
	}
	layers[1].Sandbox.Secrets[0] = "CHANGED"
	if settings.Repos["forge.example/team/*"].Sandbox.Secrets[0] != "ORG" {
		t.Fatal("returned layer data borrowed the retained settings")
	}
}

func TestHostRuleRepositoryMatchingUsesOneNormalisationAndPathCase(t *testing.T) {
	settings := DefaultSettings()
	settings.Repos = map[string]RepoSettings{
		"forge.example/Team/*":           {},
		"forge.example/team/*":           {},
		"forge.example/Team/app.git.git": {},
	}
	layers, err := matchingHostRuleRepositories(settings, "https://discarded:credential@forge.example/Team/app.git.git")
	if err != nil {
		t.Fatal(err)
	}
	if len(layers) != 2 || layers[0].Name != "forge.example/Team/*" || layers[1].Name != "forge.example/Team/app.git.git" {
		t.Fatal("repository origin was normalised twice or its path case changed", layers)
	}
	upper, err := matchingHostRuleRepositories(settings, "https://FORGE.EXAMPLE/Team/app.git.git")
	if err != nil || len(upper) != len(layers) {
		t.Fatal("an uppercase host without userinfo changed repository matching", upper, err)
	}
	for i := range layers {
		if upper[i].Name != layers[i].Name {
			t.Fatal("host normalization changed path case or removed a second suffix", upper, layers)
		}
	}
	for _, origin := range []string{"forge.example/Team-labs/app", "forge.example/team-extra/app"} {
		got, err := matchingHostRuleRepositories(settings, origin)
		if err != nil || len(got) != 0 {
			t.Fatal("a wildcard matched across a path boundary", origin, got, err)
		}
	}
}

func TestHostRuleRepositoryPatternsRejectAliasesAndMalformedUnusedSections(t *testing.T) {
	for _, repos := range []map[string]RepoSettings{
		{"forge.example/team/*": {}, "FORGE.EXAMPLE/team/*": {}},
		{"forge.example/team/*": {}, "https://forge.example/team/*": {}},
		{"forge.example/team/app": {}, "forge.example/team/*/app": {}},
		{"forge.example/team/app": {}, "forge.example/team/../other/*": {}},
	} {
		settings := DefaultSettings()
		settings.Repos = repos
		if _, err := matchingHostRuleRepositories(settings, ""); err == nil {
			t.Fatal("an unusable repository section escaped whole-document validation")
		}
	}
}

func TestHostRuleRepositorySectionsDoNotDependOnMapInsertionOrder(t *testing.T) {
	names := []string{"forge.example/Group/*", "forge.example/*", "forge.example/Group/Project", "forge.example/Group/Project/*"}
	var expected []string
	for shift := range len(names) {
		settings := DefaultSettings()
		settings.Repos = make(map[string]RepoSettings)
		for offset := range len(names) {
			settings.Repos[names[(offset+shift)%len(names)]] = RepoSettings{}
		}
		layers, err := matchingHostRuleRepositories(settings, "forge.example/Group/Project")
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, layer := range layers {
			got = append(got, layer.Name)
		}
		if shift == 0 {
			expected = got
		} else if !slices.Equal(got, expected) {
			t.Fatal("TOML table insertion order changed repository precedence", got, expected)
		}
	}
}
