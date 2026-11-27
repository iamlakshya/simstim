//go:build linux

package host

import (
	"os"
	"path/filepath"
	"testing"
)

// This owns real mapping files and anchored roots only. No fixture can issue
// an active preparation, original purge owner or positive inspection permit.
func originalGitMappingFixtureV2(t *testing.T) (*os.Root, *os.Root, worktreeOriginV1, string) {
	t.Helper()
	base := t.TempDir()
	work, common := filepath.Join(base, "work"), filepath.Join(base, "common")
	admin := filepath.Join(common, "worktrees", "task")
	for _, name := range []string{work, admin} {
		if err := os.MkdirAll(name, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range map[string]string{
		filepath.Join(work, ".git"):    "gitdir: " + admin + "\n",
		filepath.Join(admin, "HEAD"):   "ref: refs/heads/topic\n",
		filepath.Join(admin, "gitdir"): filepath.Join(work, ".git") + "\n",
	} {
		if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	workRoot, err := os.OpenRoot(work)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workRoot.Close() })
	commonRoot, err := os.OpenRoot(common)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = commonRoot.Close() })
	workRef, err := nativeWorktreeDirectoryRefV1(workRoot)
	if err != nil {
		t.Fatal(err)
	}
	commonRef, err := nativeWorktreeDirectoryRefV1(commonRoot)
	if err != nil {
		t.Fatal(err)
	}
	return workRoot, commonRoot, worktreeOriginV1{Directory: workRef, CommonRef: commonRef, Common: common, Branch: "topic"}, admin
}

func TestOriginalGitMappingPinsRealControlFilesV2(t *testing.T) {
	work, common, origin, _ := originalGitMappingFixtureV2(t)
	mapping, err := captureOrdinaryGitMappingV2(work, common, origin)
	if err != nil {
		t.Fatal(err)
	}
	defer mapping.Close()
	if err := mapping.Check(); err != nil {
		t.Fatal(err)
	}
	text, err := os.ReadFile(filepath.Join(work.Name(), ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(work.Name(), ".git"), filepath.Join(work.Name(), ".git.original")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work.Name(), ".git"), text, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := mapping.Check(); err == nil {
		t.Fatal("same-text replacement Git mapping renewed original descriptor")
	}
}

func TestOriginalGitMappingRefusesAdministrativeRedirectionV2(t *testing.T) {
	work, common, origin, admin := originalGitMappingFixtureV2(t)
	outside := t.TempDir()
	if err := os.Rename(admin, admin+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, admin); err != nil {
		t.Fatal(err)
	}
	if mapping, err := captureOrdinaryGitMappingV2(work, common, origin); mapping != nil || err == nil {
		if mapping != nil {
			_ = mapping.Close()
		}
		t.Fatal("administrative symlink escaped the retained original common root")
	}
}

func TestOriginalGitMappingRechecksBranchAndNamedAdminV2(t *testing.T) {
	for _, change := range []string{"HEAD", "directory"} {
		t.Run(change, func(t *testing.T) {
			work, common, origin, admin := originalGitMappingFixtureV2(t)
			mapping, err := captureOrdinaryGitMappingV2(work, common, origin)
			if err != nil {
				t.Fatal(err)
			}
			defer mapping.Close()
			if change == "HEAD" {
				if err := os.WriteFile(filepath.Join(admin, "HEAD"), []byte("ref: refs/heads/other\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Rename(admin, admin+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(admin, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := mapping.Check(); err == nil {
				t.Fatal("changed original Git mapping passed its final native recheck")
			}
		})
	}
}

func TestOriginalGitMappingRefusesForeignGitdirBeforeAdminOpenV2(t *testing.T) {
	work, common, origin, _ := originalGitMappingFixtureV2(t)
	if err := os.WriteFile(filepath.Join(work.Name(), ".git"), []byte("gitdir: "+t.TempDir()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if mapping, err := captureOrdinaryGitMappingV2(work, common, origin); mapping != nil || err == nil {
		if mapping != nil {
			_ = mapping.Close()
		}
		t.Fatal("foreign administrative path became a rooted original Git mapping")
	}
}
