package client

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func hostRuleSettingsFixture(t *testing.T) *settingsOperation {
	t.Helper()
	stateFDTestHome(t)
	operation, err := openSettingsOperation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := operation.Close(); err != nil {
			t.Error(err)
		}
	})
	return operation
}

func TestHostRuleOriginUsesFirstSeedEvenWithHostDirectory(t *testing.T) {
	operation := hostRuleSettingsFixture(t)
	cwd, seed, secondary := laptopSeedFixture(t), laptopSeedFixture(t), laptopSeedFixture(t)
	pullFixtureGit(t, cwd, "remote", "add", "origin", "git@forge.example:team/current.git")
	pullFixtureGit(t, seed, "remote", "add", "origin", "git@forge.example:team/seed.git")
	pullFixtureGit(t, secondary, "remote", "add", "origin", "git@forge.example:team/secondary.git")
	relative, err := filepath.Rel(cwd, seed)
	if err != nil {
		t.Fatal(err)
	}
	options := NewInputOptions{OriginalCWD: cwd, Seeds: []string{relative, secondary}, HostClone: true}
	got, err := readHostRuleOrigin(t.Context(), operation, options)
	if err != nil || got.origin != "git@forge.example:team/seed.git" || got.originalCwd != cwd {
		t.Fatal("the current folder, host directory or a secondary seed picked the host", got, err)
	}
	if err := operation.Revalidate(t.Context()); err != nil {
		t.Fatal("the metadata borrower closed its original source", err)
	}
}

func TestHostRuleOriginUsesCurrentLocalRepositoryOrScratch(t *testing.T) {
	operation := hostRuleSettingsFixture(t)
	repo := laptopSeedFixture(t)
	pullFixtureGit(t, repo, "remote", "add", "origin", "https://forge.example/team/current.git")
	nested := filepath.Join(repo, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := readHostRuleOrigin(t.Context(), operation, NewInputOptions{OriginalCWD: nested, HostClone: true})
	if err != nil || got.origin != "https://forge.example/team/current.git" || got.originalCwd != nested {
		t.Fatal("a local nested directory lost its actual origin", got, err)
	}
	scratch := t.TempDir()
	got, err = readHostRuleOrigin(t.Context(), operation, NewInputOptions{OriginalCWD: scratch})
	if err != nil || got.origin != "" || got.originalCwd != scratch {
		t.Fatal("a scratch directory manufactured repository provenance", got, err)
	}
}

func TestHostRuleOriginRefusesSecretNamespaceBeforeNativeLookup(t *testing.T) {
	stateFDTestHome(t)
	if err := os.MkdirAll(ConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath(), []byte("[secrets]\nPATH = 'never-run-this-command'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	operation, err := openSettingsOperation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer operation.Close()
	_, err = readHostRuleOrigin(t.Context(), operation, NewInputOptions{OriginalCWD: filepath.Join(t.TempDir(), "not-present"), Seeds: []string{"not-present"}})
	var user *UserError
	if !errors.As(err, &user) || user.Code != "git_environment_secret" {
		t.Fatal("a host rule entered native source lookup before the whole secret-namespace check", err)
	}
}

func TestHostRuleOriginNeedsTheActualOpenSource(t *testing.T) {
	operation := hostRuleSettingsFixture(t)
	if _, err := readHostRuleOrigin(t.Context(), nil, NewInputOptions{}); err == nil {
		t.Fatal("metadata strings replaced the original settings source")
	}
	if err := operation.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readHostRuleOrigin(t.Context(), operation, NewInputOptions{OriginalCWD: t.TempDir()}); err == nil {
		t.Fatal("a closed original source was renewed for host selection")
	}
}
