package host

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOriginalWorktreeIncludeCopyPreservesReplacement(t *testing.T) {
	isolatedStoreHostConstruction(t)
	skipOnGit254(t)
	repo := setupRepo(t)
	if err := os.WriteFile(filepath.Join(repo, includeFile), []byte(".env\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("public-test-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const id = "copy2345"
	git, err := addWorktree(&worktreeAddArgs{Dir: repo, Name: "copy", ID: id}, nil)
	if err != nil {
		t.Fatal(err)
	}
	original := git.Worktree + "-original"
	before := afterLstat
	defer func() { afterLstat = before }()
	replaced := false
	afterLstat = func(name string) {
		if name != includeFile || replaced {
			return
		}
		replaced = true
		if err := os.Rename(git.Worktree, original); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(git.Worktree, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(git.Worktree, "keep"), []byte("unrelated\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	skipped := copyOriginalWorktreeIncludeV1(git, id)
	if !replaced || len(skipped) == 0 {
		t.Fatal("the original include copy did not observe its replaced native target")
	}
	if _, err := os.Lstat(filepath.Join(git.Worktree, ".env")); !os.IsNotExist(err) {
		t.Fatal("the original copy wrote into the replacement directory", err)
	}
	if value, err := os.ReadFile(filepath.Join(git.Worktree, "keep")); err != nil || string(value) != "unrelated\n" {
		t.Fatal("the replacement directory changed", err)
	}
}
