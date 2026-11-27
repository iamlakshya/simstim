//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/jail"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

func seedFixtureGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	bin, err := systemGit()
	if err != nil {
		t.Skip(err)
	}
	cmd := exec.Command(bin, SafeGitArgs(dir, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture Git: %v: %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func seedFixture(t *testing.T) string {
	t.Helper()
	source := t.TempDir()
	seedFixtureGit(t, source, "init", "--initial-branch=chosen", "--template=")
	for _, revision := range []string{"first", "second"} {
		if err := os.WriteFile(filepath.Join(source, "tracked"), []byte(revision), 0600); err != nil {
			t.Fatal(err)
		}
		seedFixtureGit(t, source, "add", "tracked")
		seedFixtureGit(t, source, "commit", "-m", revision)
	}
	seedFixtureGit(t, source, "branch", "other")
	if err := os.WriteFile(filepath.Join(source, "tracked"), []byte("uncommitted"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, ".worktreeinclude"), []byte(".env\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, ".env"), []byte("EXPLICIT_FIXTURE=1"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "untracked"), []byte("must-not-seed"), 0600); err != nil {
		t.Fatal(err)
	}
	seedFixtureGit(t, source, "config", "core.fsmonitor", "touch fsmonitor-ran")
	seedFixtureGit(t, source, "config", "fixture.hostOnly", "must-not-seed")
	seedFixtureGit(t, source, "config", "remote.origin.url", "https://fixture:must-not-copy@example.invalid/team/project.git?credential=must-not-copy")
	if err := os.WriteFile(filepath.Join(source, ".git", "hooks", "post-checkout"), []byte("#!/bin/sh\ntouch hook-ran\n"), 0700); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Join(source, ".git", "hooks"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(source, ".git", "hooks", "post-checkout"), []byte("#!/bin/sh\ntouch hook-ran\n"), 0700); err != nil {
			t.Fatal(err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	return source
}

func TestSeedBundleExportsOnlyTheSelectedBranchHistory(t *testing.T) {
	requireSeedJail(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	source := seedFixture(t)
	beforeRoot, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(source, ".git", "config")
	if err := os.Chmod(config, 0600); err != nil {
		t.Fatal(err)
	}
	beforeConfig, err := os.Stat(config)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := createSeed(NewArgs{}, "seedone2", "fixture", source)
	if err != nil {
		t.Fatal(err)
	}
	afterRoot, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	afterConfig, err := os.Stat(config)
	if err != nil {
		t.Fatal(err)
	}
	if beforeRoot.Mode() != afterRoot.Mode() || beforeConfig.Mode() != afterConfig.Mode() || !os.SameFile(beforeRoot, afterRoot) || !os.SameFile(beforeConfig, afterConfig) {
		t.Fatal("source permissions or metadata identity changed")
	}
	if seed.Origin != "https://example.invalid/team/project.git" {
		t.Fatal("origin retained credentials or URL decorations", seed.Origin)
	}
	if seed.Scratch || seed.Repo == "" || seed.SourceBranch != "chosen" || seed.BundleSize == 0 {
		t.Fatalf("bad seed: %+v", seed)
	}
	bundle := filepath.Join(paths.SessionDir("seedone2"), "seed.bundle")
	heads := seedFixtureGit(t, t.TempDir(), "bundle", "list-heads", bundle)
	if strings.Count(heads, "\n") != 0 || !strings.HasSuffix(heads, " refs/heads/chosen") {
		t.Fatalf("extra bundle refs: %q", heads)
	}
	clone := filepath.Join(t.TempDir(), "clone")
	seedFixtureGit(t, t.TempDir(), "clone", "--template=", "--", bundle, clone)
	if got := seedFixtureGit(t, clone, "log", "--format=%s"); got != "second\nfirst" {
		t.Fatal(got)
	}
	tracked, err := os.ReadFile(filepath.Join(clone, "tracked"))
	if err != nil || string(tracked) != "second" {
		t.Fatal("uncommitted worktree content entered seed", string(tracked), err)
	}
	for _, name := range []string{"untracked", ".env", ".worktreeinclude", ".git/hooks/post-checkout"} {
		if _, err := os.Stat(filepath.Join(clone, name)); !os.IsNotExist(err) {
			t.Fatal("untracked or host metadata entered the clone", name)
		}
	}
	configBytes, err := os.ReadFile(filepath.Join(clone, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(configBytes), "must-not-seed") {
		t.Fatal("host Git config entered clone")
	}
	if _, err := os.Stat(filepath.Join(clone, "hook-ran")); !os.IsNotExist(err) {
		t.Fatal("source checkout hook ran")
	}
	if len(seed.Includes) != 0 {
		t.Fatal(".worktreeinclude opted itself in")
	}
	if _, err := os.Stat(filepath.Join(source, "fsmonitor-ran")); !os.IsNotExist(err) {
		t.Fatal("source fsmonitor ran")
	}
}

func TestSeedIncludesNeedAnExplicitGrantAndStayRooted(t *testing.T) {
	requireSeedJail(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	source := seedFixture(t)
	if err := os.Symlink(".env", filepath.Join(source, "contained")); err != nil {
		t.Fatal(err)
	}
	seed, err := createSeed(NewArgs{Include: []string{"contained"}}, "seedtwo3", "fixture", source)
	if err != nil {
		t.Fatal(err)
	}
	if len(seed.Includes) != 1 || seed.Includes[0].Path != "contained" {
		t.Fatal(seed.Includes)
	}
	got, err := os.ReadFile(filepath.Join(paths.SessionDir("seedtwo3"), "seed-includes", "contained"))
	if err != nil || string(got) != "EXPLICIT_FIXTURE=1" {
		t.Fatalf("include: %q %v", got, err)
	}
	if err := os.Symlink(".git/config", filepath.Join(source, "config-alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := createSeed(NewArgs{Include: []string{"config-alias"}}, "seedbad5", "fixture", source); err == nil {
		t.Fatal("include imported host Git config through an alias")
	}
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(source, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := createSeed(NewArgs{Include: []string{"escape"}}, "seedbad4", "fixture", source); err == nil {
		t.Fatal("include escaped repository root")
	}
}

func TestSeedScratchDoesNotCopyTheSourceFolder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "secret"), []byte("must-stay-on-host"), 0600); err != nil {
		t.Fatal(err)
	}
	seed, err := createSeed(NewArgs{}, "scratch2", "fixture", source)
	if err != nil || !seed.Scratch || seed.Repo != "scratch" || seed.BundleSize != 0 {
		t.Fatalf("%+v %v", seed, err)
	}
}

func TestSeedJailRefusalNeverRunsGitOutsideTheJail(t *testing.T) {
	source, scratch := t.TempDir(), t.TempDir()
	previous := seedJailCommand
	t.Cleanup(func() { seedJailCommand = previous })
	sentinel := errors.New("fixture jail refused")
	var seen jail.Spec
	seedJailCommand = func(_ context.Context, s jail.Spec) (*jail.Cmd, error) { seen = s; return nil, sentinel }
	_, err := seedGit(source, nil, scratch, source, "rev-parse", "HEAD")
	if !errors.Is(err, sentinel) {
		t.Fatalf("jail refusal was hidden: %v", err)
	}
	if seen.Subuid || seen.Net != jail.NetNone || seen.Seccomp != jail.ProfileGit {
		t.Fatalf("seed jail lost confinement: %+v", seen)
	}
	for _, b := range seen.Binds {
		if b.Path == source {
			t.Fatal("a graph operation bound the whole working tree")
		}
	}
	if !slices.Contains(seen.Argv, "core.hooksPath=/dev/null") || !slices.Contains(seen.Argv, "core.fsmonitor=false") {
		t.Fatal("seed omitted shared safe Git arguments")
	}
}

func TestSeedBaseCommitKeepsOnlyItsAncestry(t *testing.T) {
	requireSeedJail(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	source := seedFixture(t)
	first := seedFixtureGit(t, source, "rev-parse", "HEAD^")
	seed, err := createSeed(NewArgs{Base: first}, "seedbase", "fixture", source)
	if err != nil {
		t.Fatal(err)
	}
	if seed.Commit != first || seed.SourceBranch != "seed" {
		t.Fatalf("raw base was not preserved: %+v", seed)
	}
	clone := filepath.Join(t.TempDir(), "clone")
	seedFixtureGit(t, t.TempDir(), "clone", "--template=", "--", filepath.Join(paths.SessionDir("seedbase"), "seed.bundle"), clone)
	if got := seedFixtureGit(t, clone, "log", "--format=%s"); got != "first" {
		t.Fatalf("seed exported commits after its base: %q", got)
	}
}

func requireSeedJail(t *testing.T) {
	t.Helper()
	if !jail.Userns() {
		t.Skip("the real seed witness needs a host where the canonical Git jail has user namespaces")
	}
	version := seedFixtureGit(t, t.TempDir(), "version")
	if err := seedVersion(version); err != nil {
		t.Skipf("the real seed witness needs a supported system Git: %v", err)
	}
}

func TestSeedVersionRefusesUncontrollableHooks(t *testing.T) {
	for _, version := range []string{"git version 2.54.0", "git version 2.54.9", "unreadable"} {
		var pe *proto.Error
		if err := seedVersion(version); !errors.As(err, &pe) || pe.Code != "git_version" || pe.Hint == "" {
			t.Fatalf("unsafe Git version accepted: %q %v", version, err)
		}
	}
	for _, version := range []string{"git version 2.43.0", "git version 2.55.0", "git version 2.56.1"} {
		if err := seedVersion(version); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSeedLinkedWorktreeKeepsItsSelectedHistory(t *testing.T) {
	requireSeedJail(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	source := seedFixture(t)
	worktree := filepath.Join(t.TempDir(), "linked")
	seedFixtureGit(t, source, "worktree", "add", "-b", "linked-branch", worktree, "HEAD^")
	seed, err := createSeed(NewArgs{}, "linkedab", "fixture", worktree)
	if err != nil {
		t.Fatal(err)
	}
	if seed.SourceBranch != "linked-branch" {
		t.Fatal(seed.SourceBranch)
	}
	clone := filepath.Join(t.TempDir(), "clone")
	seedFixtureGit(t, t.TempDir(), "clone", "--template=", "--", filepath.Join(paths.SessionDir("linkedab"), "seed.bundle"), clone)
	if got := seedFixtureGit(t, clone, "log", "--format=%s"); got != "first" {
		t.Fatalf("linked source lost its selected history: %q", got)
	}
}

func TestSeedHomeRootProjectsOnlyGitMetadata(t *testing.T) {
	requireSeedJail(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	seedFixtureGit(t, home, "init", "--template=", "--initial-branch=chosen")
	if err := os.WriteFile(filepath.Join(home, "tracked"), []byte("committed"), 0600); err != nil {
		t.Fatal(err)
	}
	seedFixtureGit(t, home, "add", "tracked")
	seedFixtureGit(t, home, "commit", "-m", "home history")
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "private-fixture"), []byte("must-stay-out"), 0600); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	previous := seedJailCommand
	t.Cleanup(func() { seedJailCommand = previous })
	seedJailCommand = func(ctx context.Context, s jail.Spec) (*jail.Cmd, error) {
		for _, bind := range s.Binds {
			if bind.Path == canonical {
				t.Fatal("the source HOME was bound instead of its Git metadata")
			}
		}
		return previous(ctx, s)
	}
	seed, err := createSeed(NewArgs{}, "homehist", "fixture", home)
	if err != nil {
		t.Fatal(err)
	}
	clone := filepath.Join(t.TempDir(), "clone")
	seedFixtureGit(t, t.TempDir(), "clone", "--template=", "--", filepath.Join(paths.SessionDir("homehist"), "seed.bundle"), clone)
	if got := seedFixtureGit(t, clone, "log", "--format=%s"); got != "home history" {
		t.Fatal(got)
	}
	if _, err := os.Stat(filepath.Join(clone, ".ssh")); !os.IsNotExist(err) {
		t.Fatal("untracked HOME files entered the clone")
	}
	if seed.Scratch {
		t.Fatal("HOME repository became scratch")
	}
}

func TestSeedBarePrivateRepositoryKeepsHistoryAndPermissions(t *testing.T) {
	requireSeedJail(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	source := seedFixture(t)
	bare := filepath.Join(t.TempDir(), "source.git")
	seedFixtureGit(t, t.TempDir(), "clone", "--bare", "--template=", "--", source, bare)
	if err := os.Chmod(bare, 0700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(bare, "config")
	if err := os.Chmod(config, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(config)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := createSeed(NewArgs{}, "barehist", "fixture", bare)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(config)
	if err != nil || before.Mode() != after.Mode() || !os.SameFile(before, after) {
		t.Fatal("bare metadata permissions or identity changed", err)
	}
	clone := filepath.Join(t.TempDir(), "clone")
	seedFixtureGit(t, t.TempDir(), "clone", "--template=", "--", filepath.Join(paths.SessionDir("barehist"), "seed.bundle"), clone)
	if got := seedFixtureGit(t, clone, "log", "--format=%s"); got != "second\nfirst" {
		t.Fatal(got)
	}
	if seed.SourceBranch != "chosen" {
		t.Fatal(seed.SourceBranch)
	}
}

func TestSeedSharedCloneProjectsItsRequiredObjectAlternate(t *testing.T) {
	requireSeedJail(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	source := seedFixture(t)
	shared := filepath.Join(t.TempDir(), "shared")
	seedFixtureGit(t, t.TempDir(), "clone", "--shared", "--template=", "--", source, shared)
	if _, err := os.Stat(filepath.Join(shared, ".git", "objects", "info", "alternates")); err != nil {
		t.Fatal(err)
	}
	_, metadata, err := seedRoot(shared)
	if err != nil {
		t.Fatal(err)
	}
	objects, err := filepath.EvalSymlinks(filepath.Join(source, ".git", "objects"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(metadata, objects) {
		t.Fatal("required alternate object root was omitted", metadata)
	}
	seed, err := createSeed(NewArgs{}, "althisto", "fixture", shared)
	if err != nil {
		t.Fatal(err)
	}
	clone := filepath.Join(t.TempDir(), "clone")
	seedFixtureGit(t, t.TempDir(), "clone", "--template=", "--", filepath.Join(paths.SessionDir("althisto"), "seed.bundle"), clone)
	if got := seedFixtureGit(t, clone, "log", "--format=%s"); got != "second\nfirst" {
		t.Fatal(got)
	}
	if seed.Commit == "" {
		t.Fatal("alternate-backed source has no commit")
	}
}

func TestSeedKeepsTheLiteralRepositoryBasename(t *testing.T) {
	requireSeedJail(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	source := seedFixture(t)
	literal := filepath.Join(t.TempDir(), "source: team")
	if err := os.Rename(source, literal); err != nil {
		t.Fatal(err)
	}
	seed, err := createSeed(NewArgs{}, "seedlit2", "fixture", literal)
	if err != nil {
		t.Fatal(err)
	}
	if seed.Repo != "source: team" {
		t.Fatalf("fresh source basename was rewritten: %q", seed.Repo)
	}
}
