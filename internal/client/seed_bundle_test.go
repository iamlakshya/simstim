package client

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func laptopSeedFixture(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "project")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	pullFixtureGit(t, dir, "init", "--template=", "--initial-branch=main")
	pullFixtureGit(t, dir, "config", "user.name", "Fixture")
	pullFixtureGit(t, dir, "config", "user.email", "fixture@example.invalid")
	pullFixtureCommit(t, dir, "tracked.txt", "committed\n")
	pullFixtureGit(t, dir, "branch", "unrelated")
	pullFixtureGit(t, dir, "tag", "private-tag")
	return dir
}

func fixtureLaptopSeed(t *testing.T, originalCwd, source string, dirty bool, limit int64) (*laptopSeed, error) {
	t.Helper()
	identity, err := readCommandGitIdentity(t.Context(), originalCwd)
	if err != nil {
		return nil, err
	}
	return createLaptopSeed(t.Context(), source, dirty, limit, identity, "")
}

func TestLaptopDirtySeedKeepsIndexRefsAndBytesAndNeverRunsFilters(t *testing.T) {
	dir := laptopSeedFixture(t)
	copyFixture(t, dir, "tracked.txt", "working bytes\n")
	copyFixture(t, dir, "untracked.txt", "new untracked bytes\n")
	copyFixture(t, dir, ".gitignore", "ignored.txt\n")
	copyFixture(t, dir, "ignored.txt", "ignored\n")
	copyFixture(t, dir, ".gitattributes", "*.txt filter=tripwire\n")
	marker := filepath.Join(t.TempDir(), "executed")
	pullFixtureGit(t, dir, "config", "filter.tripwire.clean", "touch '"+marker+"'; cat")
	pullFixtureGit(t, dir, "config", "core.fsmonitor", "touch '"+marker+"'")
	pullFixtureGit(t, dir, "config", "hook.post-index-change.command", "touch '"+marker+"'")
	signer := filepath.Join(t.TempDir(), "signer")
	if err := os.WriteFile(signer, []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	pullFixtureGit(t, dir, "config", "commit.gpgsign", "true")
	pullFixtureGit(t, dir, "config", "gpg.program", signer)
	beforeStatus := pullFixtureGit(t, dir, "-c", "filter.tripwire.clean=cat", "status", "--porcelain=v1", "-z")
	beforeRefs := pullFixtureGit(t, dir, "show-ref")
	beforeIndex, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	beforeConfig, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	seed, err := fixtureLaptopSeed(t, dir, dir, true, api.DefaultSeedMax)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = seed.close() })
	afterIndex, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil || !bytes.Equal(beforeIndex, afterIndex) {
		t.Fatalf("original index changed: %v", err)
	}
	afterConfig, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
	if err != nil || !bytes.Equal(beforeConfig, afterConfig) {
		t.Fatalf("original config changed: %v", err)
	}
	if got := pullFixtureGit(t, dir, "show-ref"); got != beforeRefs {
		t.Fatal("original refs changed")
	}
	if got := pullFixtureGit(t, dir, "-c", "filter.tripwire.clean=cat", "status", "--porcelain=v1", "-z"); got != beforeStatus {
		t.Fatal("original status changed")
	}
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("configured command ran: %v", err)
	}
	if got := pullFixtureGit(t, dir, "show", seed.declaration.Commit+":tracked.txt"); got != "working bytes" {
		t.Fatalf("filtered or stale bytes: %q", got)
	}
	if !slices.Contains(seed.untracked, "untracked.txt") || slices.Contains(seed.untracked, "ignored.txt") {
		t.Fatalf("actual untracked set = %q", seed.untracked)
	}
	heads := pullFixtureGit(t, dir, "bundle", "list-heads", seed.bundle)
	if strings.Contains(heads, "private-tag") || strings.Contains(heads, "unrelated") || strings.Count(heads, "refs/") != 1 {
		t.Fatalf("unrelated refs leaked: %s", heads)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "tracked.txt")); err != nil || string(data) != "working bytes\n" {
		t.Fatal("original working file changed")
	}
}

func TestLaptopDirtySeedRefusesSensitiveAndSkipsMissingTrackedFile(t *testing.T) {
	dir := laptopSeedFixture(t)
	if err := os.Remove(filepath.Join(dir, "tracked.txt")); err != nil {
		t.Fatal(err)
	}
	seed, err := fixtureLaptopSeed(t, dir, dir, true, api.DefaultSeedMax)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = seed.close() })
	if output := pullFixtureGit(t, dir, "ls-tree", "--name-only", seed.declaration.Commit); strings.Contains(output, "tracked.txt") {
		t.Fatal("missing tracked file was read or restored")
	}
	copyFixture(t, dir, ".env", "do not send")
	if bad, err := fixtureLaptopSeed(t, dir, dir, true, api.DefaultSeedMax); err == nil {
		_ = bad.close()
		t.Fatal("sensitive untracked path accepted")
	}
}

func TestLaptopSeedCapIsPerBundleAndRecheckedWhileStreaming(t *testing.T) {
	dir := laptopSeedFixture(t)
	if seed, err := fixtureLaptopSeed(t, dir, dir, false, 1); err == nil {
		_ = seed.close()
		t.Fatal("writing cap ignored")
	}
	seed, err := fixtureLaptopSeed(t, dir, dir, false, api.DefaultSeedMax)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = seed.close() })
	var out bytes.Buffer
	if err = seed.write(t.Context(), &out, seed.declaration.Size-1); err == nil || out.Len() != 0 {
		t.Fatal("stream exceeded current cap")
	}
	if err = seed.write(t.Context(), &out, seed.declaration.Size); err != nil || int64(out.Len()) != seed.declaration.Size {
		t.Fatalf("exact stream = %d, %v", out.Len(), err)
	}
}
