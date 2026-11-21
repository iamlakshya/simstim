package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

func autoPreparedHostFixtureV1(t *testing.T, floor string) Host {
	t.Helper()
	host := h5GrantState(t)
	host.WallFloor = floor
	if err := SaveHosts(t.Context(), []Host{host}); err != nil {
		t.Fatal(err)
	}
	return host
}

func TestAutoLocalNeedIncludesClassifierAndSSHKeyMinima(t *testing.T) {
	for _, source := range []string{"env", "classifier", "ssh-key"} {
		t.Run(source, func(t *testing.T) {
			host := autoPreparedHostFixtureV1(t, "microvm").WithPool(true)
			if err := SaveHosts(t.Context(), []Host{host}); err != nil {
				t.Fatal(err)
			}
			settings := DefaultSettings()
			settings.Secrets = map[string]SecretDefinition{"TOKEN": {Run: "this-value-reader-must-never-run"}}
			values := api.LayerValues{}
			switch source {
			case "env":
				values.Sandbox.Secrets = []string{"TOKEN"}
			case "classifier":
				key := "TOKEN"
				values.Classifier.Key = &key
			case "ssh-key":
				values.Sandbox.SSHKeys = []string{"TOKEN"}
			}
			p := &AutoPlacementPlanV1{args: NewArgs{Agent: "shell"}, options: NewInputOptions{Session: values}, need: PlacementNeed{Tier: TierShared}}
			candidate, reason, err := p.localCandidateV1(t.Context(), settings, host, TierShared)
			if err != nil || candidate != nil || reason == "" {
				t.Fatal("selected credential escaped local tier filtering", candidate, reason, err)
			}
			settings.SecretTiers = map[string]string{"TOKEN": "shared"}
			candidate, reason, err = p.localCandidateV1(t.Context(), settings, host, TierShared)
			if err != nil || reason != "" || candidate == nil || !candidate.pendingGrants {
				t.Fatal("eligible shared credential lost its unanswered-question metadata", reason, err)
			}
		})
	}
}

func TestAutoLocalNeedUsesCandidateLayerSizeAndOriginalMemoryCap(t *testing.T) {
	host := autoPreparedHostFixtureV1(t, "microvm")
	memory := "7G"
	host.LayerValues.Sandbox.Memory = &memory
	if err := SaveHosts(t.Context(), []Host{host}); err != nil {
		t.Fatal(err)
	}
	p := &AutoPlacementPlanV1{args: NewArgs{Agent: "shell"}}
	candidate, reason, err := p.localCandidateV1(t.Context(), DefaultSettings(), host, TierOwn)
	if err != nil || reason != "" || candidate == nil {
		t.Fatal(reason, err)
	}
	want, err := api.ParseSize(memory)
	if err != nil || candidate.need.Memory != want || candidate.need.MinLevel != "microvm" {
		t.Fatal("host layer size did not reach actual need", candidate.need, err)
	}
	budget, err := vmneed.BudgetFor(want)
	if err != nil {
		t.Fatal(err)
	}
	p.args.MemoryMax = budget.Limit - 1
	if candidate, reason, err := p.localCandidateV1(t.Context(), DefaultSettings(), host, TierOwn); err != nil || candidate != nil || reason == "" {
		t.Fatal("an incompatible original cap was omitted from local selection", reason, err)
	}
	p.args.MemoryMax = budget.Limit
	if candidate, reason, err := p.localCandidateV1(t.Context(), DefaultSettings(), host, TierOwn); err != nil || candidate == nil || reason != "" {
		t.Fatal(reason, err)
	}
}

func TestAutoExplicitSelectorStillReadsActualLocalPolicyOriginBeforeContact(t *testing.T) {
	hostSelectionConfig(t, "[secrets]\nTOKEN = 'this-value-reader-must-never-run'\n[repos.'forge.example/team/app'.sandbox]\nsecrets = ['TOKEN']\n")
	repository := laptopSeedFixture(t)
	pullFixtureGit(t, repository, "remote", "add", "origin", "git@forge.example:team/app.git")
	hosts, err := LoadHosts()
	if err != nil {
		t.Fatal(err)
	}
	for i := range hosts {
		hosts[i] = hosts[i].WithPool(false)
	}
	if err := SaveHosts(t.Context(), hosts); err != nil {
		t.Fatal(err)
	}
	selection, err := PrepareHostSelection(t.Context(), "auto", NewInputOptions{OriginalCWD: repository})
	if err != nil {
		t.Fatal(err)
	}
	defer selection.Close()
	plan, err := PrepareAutoPlacementV1(t.Context(), selection, NewArgs{Agent: "shell"}, PlacementNeed{})
	if plan != nil {
		defer plan.Close(t.Context())
	}
	if err != nil || plan == nil || plan.origin != "forge.example/team/app" {
		t.Fatal("explicit auto skipped its local credential-policy origin", err)
	}
	if len(plan.Names()) != 0 {
		t.Fatal("excluded pool hosts became candidates")
	}
}

func TestAutoSelectedCredentialOnHostsRefusalDoesNotReadAValue(t *testing.T) {
	host := autoPreparedHostFixtureV1(t, "microvm")
	marker := filepath.Join(t.TempDir(), "forbidden-reader")
	settings := DefaultSettings()
	settings.Secrets = map[string]SecretDefinition{"TOKEN": {Run: "touch " + ShellWord(marker), OnHosts: []string{"another"}}}
	p := &AutoPlacementPlanV1{args: NewArgs{Agent: "shell"}, options: NewInputOptions{Session: api.LayerValues{Sandbox: api.SandboxValues{SSHKeys: []string{"TOKEN"}}}}}
	if candidate, reason, err := p.localCandidateV1(t.Context(), settings, host, TierOwn); err != nil || candidate != nil || reason == "" {
		t.Fatal(reason, err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("selection ran a credential reader", err)
	}
}

func TestAutoCompletesAllCredentialMinimaBeforeActualProviderStatus(t *testing.T) {
	for _, source := range []string{"env", "classifier", "ssh-key"} {
		t.Run(source, func(t *testing.T) {
			host := autoPreparedHostFixtureV1(t, "microvm").WithPool(true)
			folder := t.TempDir()
			marker, script := filepath.Join(folder, "status-ran"), filepath.Join(folder, "provider")
			body := fmt.Sprintf("#!/bin/sh\n: > %s\nprintf '{\"state\":\"running\"}'\n", ShellWord(marker))
			if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
				t.Fatal(err)
			}
			host.Provider = &Provider{Script: script, WakeOnAttach: true, Cost: "paid"}
			if err := SaveHosts(t.Context(), []Host{host}); err != nil {
				t.Fatal(err)
			}
			if err := SetTierContext(t.Context(), host.Name, TierShared, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ""); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(settingsPath(), []byte("[secrets]\nTOKEN = 'this-value-reader-must-never-run'\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			values := api.LayerValues{}
			switch source {
			case "env":
				values.Sandbox.Secrets = []string{"TOKEN"}
			case "classifier":
				key := "TOKEN"
				values.Classifier.Key = &key
			case "ssh-key":
				values.Sandbox.SSHKeys = []string{"TOKEN"}
			}
			selection, err := PrepareHostSelection(t.Context(), "auto", NewInputOptions{OriginalCWD: folder, Session: values})
			if err != nil {
				t.Fatal(err)
			}
			defer selection.Close()
			plan, err := PrepareAutoPlacementV1(t.Context(), selection, NewArgs{Agent: "shell"}, PlacementNeed{Tier: TierShared})
			if plan != nil {
				defer plan.Close(t.Context())
			}
			if err != nil || plan == nil || len(plan.Names()) != 0 {
				t.Fatal("ineligible credential host entered automatic gathering", err)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("provider ran before complete local credential filtering", err)
			}
		})
	}
}

func TestAutoNamesOwnTheRankedSliceAndFailedPreparationExposesNone(t *testing.T) {
	p := &AutoPlacementPlanV1{ctx: context.Background(), prepared: true,
		candidates: []*autoPlacementCandidate{{host: Host{Name: "box"}}, {host: Host{Name: "other"}}}}
	names := p.Names()
	names[0] = "changed"
	if !slices.Equal(p.Names(), []string{"box", "other"}) {
		t.Fatal("caller mutated original ranked targets")
	}
	p.prepared = false
	if len(p.Names()) != 0 {
		t.Fatal("partly gathered hosts escaped failed preparation")
	}
}
