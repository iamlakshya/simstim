package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

func hostSelectionConfig(t *testing.T, text string) {
	t.Helper()
	placementHostsFixture(t)
	t.Setenv(brand.Env("DEFAULT_HOST"), "")
	if err := os.WriteFile(settingsPath(), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func closeHostSelection(t *testing.T, selection *HostSelectionV1) {
	t.Helper()
	t.Cleanup(func() {
		if err := selection.Close(); err != nil {
			t.Error(err)
		}
	})
}

func TestHostSelectionFacadeUsesEffectiveProfileAndOwnsItsNames(t *testing.T) {
	hostSelectionConfig(t, "[profiles.review.sandbox]\nhost = ['other', 'box']\n")
	selection, err := PrepareHostSelection(t.Context(), "", NewInputOptions{OriginalCWD: t.TempDir(), Profile: "review"})
	if err != nil {
		t.Fatal(err)
	}
	closeHostSelection(t, selection)
	if !slices.Equal(selection.Names(), []string{"other", "box"}) || selection.Automatic() || selection.Reason() != "from the profile review" {
		t.Fatal("the actual local profile did not select an ordered rule", selection.Names(), selection.Reason())
	}
	names := selection.Names()
	names[0] = "changed"
	if selection.Names()[0] != "other" {
		t.Fatal("a caller changed the facade's retained metadata")
	}
	if err := selection.Check(t.Context()); err != nil {
		t.Fatal("unchanged actual source did not revalidate", err)
	}
}

func TestHostSelectionFacadeExplicitHostIgnoresLowerRuleAndOrigin(t *testing.T) {
	hostSelectionConfig(t, "[profiles.review.sandbox]\nhost = 'not-saved'\n")
	selection, err := PrepareHostSelection(t.Context(), "box", NewInputOptions{OriginalCWD: filepath.Join(t.TempDir(), "missing"), Profile: "review"})
	if err != nil {
		t.Fatal(err)
	}
	closeHostSelection(t, selection)
	if !slices.Equal(selection.Names(), []string{"box"}) || selection.Reason() != "named by the request" {
		t.Fatal("an unused rule or local origin changed an explicit request", selection.Names(), selection.Reason())
	}
}

func TestHostSelectionFacadeUsesFirstSeedBeforeHostDirectory(t *testing.T) {
	hostSelectionConfig(t, "[repos.'forge.example/team/current'.sandbox]\nhost = 'box'\n[repos.'forge.example/team/seed'.sandbox]\nhost = ['other', 'box']\n")
	cwd, seed := laptopSeedFixture(t), laptopSeedFixture(t)
	pullFixtureGit(t, cwd, "remote", "add", "origin", "git@forge.example:team/current.git")
	pullFixtureGit(t, seed, "remote", "add", "origin", "git@forge.example:team/seed.git")
	relative, err := filepath.Rel(cwd, seed)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := PrepareHostSelection(t.Context(), "", NewInputOptions{OriginalCWD: cwd, Seeds: []string{relative}, HostClone: true})
	if err != nil {
		t.Fatal(err)
	}
	closeHostSelection(t, selection)
	if !slices.Equal(selection.Names(), []string{"other", "box"}) || selection.Reason() != "from the rule for forge.example/team/seed" {
		t.Fatal("a host directory or the current repository replaced the first seed rule", selection.Names(), selection.Reason())
	}
	pullFixtureGit(t, seed, "remote", "set-url", "origin", "git@forge.example:team/current.git")
	if err := selection.Check(t.Context()); err == nil {
		t.Fatal("an origin change silently renewed a host-selection request")
	}
	if !slices.Equal(selection.Names(), []string{"other", "box"}) {
		t.Fatal("a failed check silently selected a new host")
	}
}

func TestHostSelectionFacadeUsesTheCanonicalRepositoryMatcher(t *testing.T) {
	hostSelectionConfig(t, "[repos.'forge.example/Team/app.git.git'.sandbox]\nhost = 'other'\n")
	repo := laptopSeedFixture(t)
	pullFixtureGit(t, repo, "remote", "add", "origin", "https://FORGE.EXAMPLE/Team/app.git.git")
	selection, err := PrepareHostSelection(t.Context(), "", NewInputOptions{OriginalCWD: repo})
	if err != nil {
		t.Fatal(err)
	}
	closeHostSelection(t, selection)
	if !slices.Equal(selection.Names(), []string{"other"}) {
		t.Fatal("the production resolver normalised the origin twice or skipped its canonical section", selection.Names())
	}
}

func TestHostSelectionFacadeSessionSelectorReplacesProfileAndOwnsOptions(t *testing.T) {
	hostSelectionConfig(t, "[profiles.review.sandbox]\nhost = ['other', 'box']\n")
	options := NewInputOptions{OriginalCWD: t.TempDir(), Profile: "review",
		Session: api.LayerValues{Sandbox: api.SandboxValues{Host: &api.HostSelectorV1{Names: []string{"shared"}}}}}
	selection, err := PrepareHostSelection(t.Context(), "", options)
	if err != nil {
		t.Fatal(err)
	}
	closeHostSelection(t, selection)
	options.Session.Sandbox.Host.Names[0] = "box"
	if !slices.Equal(selection.Names(), []string{"shared"}) || selection.Reason() != "from the session's host rule" {
		t.Fatal("host alternatives added up or the request borrowed mutable session options", selection.Names(), selection.Reason())
	}
	if err := selection.Check(t.Context()); err != nil {
		t.Fatal("the caller's changed data altered the captured original options", err)
	}
}

func TestHostSelectionFacadeChecksDefaultEnvironmentNamespace(t *testing.T) {
	key := brand.Env("DEFAULT_HOST")
	hostSelectionConfig(t, "[secrets]\n"+key+" = 'never-run-this-reader'\n")
	t.Setenv(key, "box")
	_, err := PrepareHostSelection(t.Context(), "", NewInputOptions{OriginalCWD: t.TempDir()})
	var user *UserError
	if !errors.As(err, &user) || user.Code != "git_environment_secret" {
		t.Fatal("the facade read a default-host key configured as a secret", err)
	}
}

func TestHostSelectionFacadeDoesNotRetargetAfterDefaultOrSettingsChange(t *testing.T) {
	for _, source := range []string{"environment", "settings"} {
		t.Run(source, func(t *testing.T) {
			hostSelectionConfig(t, "")
			t.Setenv(brand.Env("DEFAULT_HOST"), "box")
			selection, err := PrepareHostSelection(t.Context(), "", NewInputOptions{OriginalCWD: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			closeHostSelection(t, selection)
			if source == "environment" {
				t.Setenv(brand.Env("DEFAULT_HOST"), "other")
			} else if err := os.WriteFile(settingsPath(), []byte("[profiles.review.sandbox]\nhost = 'other'\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := selection.Check(t.Context()); err == nil {
				t.Fatal("a changed original source retained a successful choice")
			}
			if !slices.Equal(selection.Names(), []string{"box"}) {
				t.Fatal("a failed source check selected another host")
			}
		})
	}
}

func TestHostSelectionFacadeKeepsTheOriginalLastChoice(t *testing.T) {
	hostSelectionConfig(t, "")
	RememberLast("other", "root2345", "last")
	selection, err := PrepareHostSelection(t.Context(), "-", NewInputOptions{})
	if err != nil {
		t.Fatal(err)
	}
	closeHostSelection(t, selection)
	RememberLast("box", "next2345", "new last")
	if err := selection.Check(t.Context()); err != nil || !slices.Equal(selection.Names(), []string{"other"}) {
		t.Fatal("another attach renewed the pending last-host request", selection.Names(), err)
	}
}

func TestHostSelectionFacadeCloseAndCancellationRetireTheSource(t *testing.T) {
	hostSelectionConfig(t, "")
	ctx, cancel := context.WithCancel(t.Context())
	selection, err := PrepareHostSelection(ctx, "box", NewInputOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := selection.Check(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatal("a later context extended the original selection lifetime", err)
	}
	if err := selection.Close(); err != nil {
		t.Fatal(err)
	}
	if len(selection.Names()) != 0 || selection.Automatic() || selection.Reason() != "" {
		t.Fatal("closed selection retained usable metadata")
	}
	var empty HostSelectionV1
	if err := empty.Check(t.Context()); err == nil || len(empty.Names()) != 0 || empty.Automatic() || strings.TrimSpace(empty.Reason()) != "" {
		t.Fatal("a zero value manufactured an original selection source")
	}
}
