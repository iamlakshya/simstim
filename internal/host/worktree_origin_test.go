package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/cachefs"
)

// This runs the actual native mkdir, real prepared-ref transaction and Git
// checkout in a private test home. It starts no session, provider or manager.
func TestActualWorktreeAddRecordsNativeOriginBeforeCheckout(t *testing.T) {
	isolatedStoreHostConstruction(t)
	skipOnGit254(t)
	repo := setupRepo(t)
	const id = "abcd2345"
	planned := false
	var prepared worktreeOriginV1
	worktree, err := addWorktree(&worktreeAddArgs{Dir: repo, Name: "origin", ID: id}, func(g *GitInfo) {
		planned = true
		if _, err := os.Lstat(filepath.Join(g.Worktree, ".git")); !os.IsNotExist(err) {
			t.Fatal("the branch origin was published only after the checkout", err)
		}
		if err := withPlacement(context.Background(), false, func(control *placementControl) error {
			prepared = control.WorktreeOrigins[id]
			return nil
		}, false); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(g.Worktree)
		if err != nil || prepared.Phase != "branch-created" || !prepared.Directory.matches(info) || prepared.BranchRef == nil {
			t.Fatal("the original native directory and prepared branch did not precede checkout", err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if !planned || worktree == nil {
		t.Fatal("the actual worktree constructor omitted its original creation plan")
	}
	if err := withPlacement(context.Background(), false, func(control *placementControl) error {
		origin := control.WorktreeOrigins[id]
		if origin.Phase != "installed" || !samePurgeDirectoryRef(origin.Directory, prepared.Directory) || !footprintRefEqualV1(origin.BranchRef, prepared.BranchRef) {
			t.Fatal("the checkout renewed rather than retained the original native creation")
		}
		return nil
	}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(filepath.Join(worktree.Worktree, "."+brand.Name, "setup")); err != nil {
		t.Fatal("the actual checkout did not populate the reserved empty directory", err)
	}
	// A second actual add cannot renew the existing creation receipt, even
	// when the requested ID, path and branch descriptions are identical.
	if _, err := addWorktree(&worktreeAddArgs{Dir: repo, Name: "origin", ID: id}, nil); err == nil {
		t.Fatal("a later same-name add adopted the existing native worktree")
	}
}

func TestWorktreeOriginDescriptionsCannotSupplyNativeCreation(t *testing.T) {
	for _, raw := range []string{
		`{"relative":"app/origin-abcd2345","repository":"/app","common":"/app/.git","branch":"` + brand.Name + `/origin-abcd2345","base_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","phase":"installed"}`,
		`{"relative":"../other","phase":"directory"}`,
		`{"relative":"app/origin-abcd2345","phase":"directory","future_origin":true}`,
	} {
		var origin worktreeOriginV1
		if err := json.Unmarshal([]byte(raw), &origin); err != nil {
			t.Fatal(err)
		}
		if err := validateWorktreeOriginsV1(map[string]worktreeOriginV1{"abcd2345": origin}); err == nil {
			t.Fatal("saved descriptions supplied original native creation rights", raw)
		}
	}
}

func TestNativeWorktreeBranchCaptureRetainsRestrictiveMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native branch inode ownership is a Unix host route")
	}
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root, err := cachefs.OpenPrivateRoot(directory, false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, mode := range []os.FileMode{0o600, 0o640, 0o644} {
		name := mode.String()
		file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteString("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n"); err != nil {
			t.Fatal(err)
		}
		actual, err := file.Stat()
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		captured, ref, err := openNativeWorktreeBranchV1(root, name)
		if err != nil {
			t.Fatal("the native branch capture rejected a safely restrictive original mode", err)
		}
		if ref.Mode != uint32(actual.Mode()) || !footprintRefMatchesV1(ref, actual) {
			t.Fatal("the native branch capture changed or widened the original mode")
		}
		if err := captured.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(filepath.Join(directory, name), filepath.Join(directory, name+"-link")); err != nil {
			t.Fatal(err)
		}
		if file, _, err := openNativeWorktreeBranchV1(root, name); err == nil {
			_ = file.Close()
			t.Fatal("a multiply linked branch descriptor became native origin")
		}
	}
}
