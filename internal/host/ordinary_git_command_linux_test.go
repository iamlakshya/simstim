//go:build linux

package host

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These are native command/root primitives. They create no positive active
// session, purge permit or joined production inspection source.
func originalGitCommandFixtureV2(t *testing.T) (*ordinaryActiveGitRootsV2, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is absent")
	}
	base := t.TempDir()
	repository, work := filepath.Join(base, "repository"), filepath.Join(base, "work")
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	fixtureGit := func(args ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", repository}, args...)...)
		command.Env = ordinaryGitEnvironmentV2()
		// Fixture setup owns a new repository and does not use the production
		// descriptor slots. Remove that production command's common locator.
		var environment []string
		for _, entry := range command.Env {
			if !strings.HasPrefix(entry, "GIT_COMMON_DIR=") {
				environment = append(environment, entry)
			}
		}
		command.Env = environment
		raw, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("fixture git %v: %s: %v", args, raw, err)
		}
		return strings.TrimSpace(string(raw))
	}
	fixtureGit("init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(repository, "tracked"), []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit("add", "tracked")
	fixtureGit("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "fixture")
	fixtureGit("worktree", "add", "-b", "topic", work)
	admin := fixtureGit("-C", work, "rev-parse", "--absolute-git-dir")
	roots := &ordinaryActiveGitRootsV2{}
	for _, pair := range []struct {
		path string
		file **os.File
	}{{work, &roots.Work}, {admin, &roots.Admin}, {filepath.Join(repository, ".git"), &roots.Common}} {
		file, err := os.Open(pair.path)
		if err != nil {
			_ = roots.Close()
			t.Fatal(err)
		}
		*pair.file = file
	}
	t.Cleanup(func() { _ = roots.Close() })
	return roots, work
}

func TestOriginalOrdinaryGitUsesExplicitActualRootsV2(t *testing.T) {
	roots, work := originalGitCommandFixtureV2(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := checkOrdinaryGitVersionV2(ctx, roots); err != nil {
		t.Fatal(err)
	}
	clean, err := runOriginalOrdinaryGitV2(ctx, roots, "status", "--porcelain")
	if err != nil || clean != "" {
		t.Fatal("original new worktree is not clean", clean, err)
	}
	if err := os.WriteFile(filepath.Join(work, "untracked"), []byte("retained\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Inherited selectors cannot redirect this read to another repository.
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "foreign"))
	t.Setenv("GIT_WORK_TREE", t.TempDir())
	t.Setenv("GIT_COMMON_DIR", t.TempDir())
	dirty, err := runOriginalOrdinaryGitV2(ctx, roots, "status", "--porcelain")
	if err != nil || countLines(dirty) != 1 || !strings.Contains(dirty, "untracked") {
		t.Fatal("inherited Git scope changed original root inspection", dirty, err)
	}
	count, err := runOriginalOrdinaryGitV2(ctx, roots, "rev-list", "--count", "HEAD", "--not", "--remotes")
	if err != nil || count != "1" {
		t.Fatal("original unpushed count changed", count, err)
	}
	raw, err := os.ReadFile(filepath.Join(work, "untracked"))
	if err != nil || string(raw) != "retained\n" {
		t.Fatal("read inspection changed original untracked content", err)
	}
}

func TestOrdinaryGitResultCannotOriginateFromCountsV2(t *testing.T) {
	for _, source := range []*joinedOrdinaryGitInspectionV2{nil, {}, {complete: true, counts: purgeGitInspectionV1{}}} {
		if _, err := source.Counts(context.Background(), &SessionOperation{}, &OrdinaryPurgePreparationV2{}); err == nil {
			t.Fatal("counts or phase issued joined native inspection")
		}
	}
}
