package host_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/proto"
)

// repo makes a git repository with one commit under the home folder.
func repo(t *testing.T, h *hosttest.Host) string {
	t.Helper()
	dir := filepath.Join(h.Home, "src", "app")
	writeFile(t, filepath.Join(dir, "README"), "app\n")
	writeFile(t, filepath.Join(dir, ".gitignore"), ".env\n")
	writeFile(t, filepath.Join(dir, ".env"), "SECRET=not-in-git\n")
	writeFile(t, filepath.Join(dir, ".worktreeinclude"), "# files git ignores, for each worktree\n\n.env\n../outside\n")
	git(t, dir, "init", "-q")
	git(t, dir, "add", "README", ".gitignore", ".worktreeinclude")
	git(t, dir, "commit", "-q", "-m", "first")
	return dir
}

func TestWorktreeSessions(t *testing.T) {
	hosttest.SkipOnGit254(t)
	h := hosttest.New(t)
	dir := repo(t, h)
	s, err := host.New(host.NewArgs{Name: "Fix the auth bug!", Cwd: "~/src/app", Worktree: true, Argv: []string{"sleep", "60"}})
	if err != nil {
		t.Fatal(err)
	}
	g := manifest(t, s.ID).Git
	if g == nil || !g.CreatedBy || g.RepoRoot != dir || g.BaseRef != "HEAD" || s.Cwd != g.Worktree {
		t.Fatalf("git info = %+v, cwd %s", g, s.Cwd)
	}
	if want := brand.Name + "/fix-the-auth-bug-" + s.ID; g.Branch != want {
		t.Errorf("branch = %q, want %q", g.Branch, want)
	}
	if b, err := os.ReadFile(filepath.Join(g.Worktree, ".env")); err != nil || string(b) != "SECRET=not-in-git\n" {
		t.Errorf(".worktreeinclude did not copy .env: %q, %v", b, err)
	}
	if !strings.Contains(git(t, dir, "worktree", "list", "--porcelain"), "locked") {
		t.Error("the worktree is not locked while the session lives")
	}
	// Recent folders count a worktree as its repository.
	if f := host.Folders("", 0); !reflect.DeepEqual(f.Recent, []string{"~/src/app"}) {
		t.Errorf("recent = %q", f.Recent)
	}

	res, err := host.Delete(s.ID)
	if err != nil || res.Worktree != "removed the clean worktree and its branch" {
		t.Fatalf("delete = %+v, %v", res, err)
	}
	if _, err := os.Stat(g.Worktree); !os.IsNotExist(err) {
		t.Error("the clean worktree is still there")
	}
	if strings.Contains(git(t, dir, "branch"), g.Branch) {
		t.Error("the branch of the clean worktree is still there")
	}

	// A worktree with work in it is kept, and the result says what is in it.
	s, err = host.New(host.NewArgs{Name: "keep", Cwd: dir, Worktree: true, Base: "main", Argv: []string{"sleep", "60"}})
	if err != nil {
		t.Fatal(err)
	}
	g = manifest(t, s.ID).Git
	writeFile(t, filepath.Join(g.Worktree, "new.txt"), "work\n")
	git(t, g.Worktree, "add", "new.txt")
	git(t, g.Worktree, "commit", "-q", "-m", "work")
	writeFile(t, filepath.Join(g.Worktree, "README"), "changed\n")
	res, err = host.Delete(s.ID)
	if err != nil || !strings.Contains(res.Worktree, "kept the worktree") || !strings.Contains(res.Worktree, "1 new commit and 1 changed file") {
		t.Fatalf("delete = %+v, %v", res, err)
	}
	if _, err := os.Stat(g.Worktree); err != nil {
		t.Error("the worktree with work in it was removed")
	}
}

// An entry of .worktreeinclude that is a symlink to the user's SSH key
// copies nothing into the worktree, and the session's log says why. So does
// a pattern that leaves the repository.
func TestWorktreeIncludeSkipsASymlinkOut(t *testing.T) {
	hosttest.SkipOnGit254(t)
	h := hosttest.New(t)
	dir := repo(t, h)
	key := filepath.Join(h.Home, ".ssh", "id_ed25519")
	writeFile(t, key, "PRIVATE KEY\n")
	if err := os.Symlink(key, filepath.Join(dir, "id")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, ".worktreeinclude"), ".env\nid\n../outside\n")
	s, err := host.New(host.NewArgs{Name: "keys", Cwd: dir, Worktree: true, Argv: []string{"sleep", "60"}})
	if err != nil {
		t.Fatal(err)
	}
	wt := manifest(t, s.ID).Git.Worktree
	if _, err := os.Lstat(filepath.Join(wt, "id")); !os.IsNotExist(err) {
		t.Errorf("the symlink to the key reached the worktree: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(wt, ".env")); err != nil || string(b) != "SECRET=not-in-git\n" {
		t.Errorf(".env = %q, %v", b, err)
	}
	log := h.HolderLog(s.ID)
	for _, want := range []string{
		`msg="did not copy an entry of .worktreeinclude into the worktree" file=id why="it is a symbolic link, and only regular files are copied"`,
		`file=../outside why="the pattern is not a path inside the repository"`,
	} {
		if !strings.Contains(log, want) {
			t.Errorf("the session's log does not say %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "file=.env") {
		t.Errorf("the log lists .env, which was copied:\n%s", log)
	}
}

func TestWorktreeNeedsARepositoryAndABase(t *testing.T) {
	h := hosttest.New(t)
	repo(t, h)
	for _, c := range []struct {
		args host.NewArgs
		code string
	}{
		{host.NewArgs{Cwd: "~", Worktree: true}, "not_a_repo"},
		{host.NewArgs{Cwd: "~/src/app", Worktree: true, Base: "no-such-branch"}, "bad_base"},
	} {
		c.args.Argv = []string{"true"}
		_, err := host.New(c.args)
		var pe *proto.Error
		if !errors.As(err, &pe) || pe.Code != c.code {
			t.Errorf("new %+v = %v, want %s", c.args, err, c.code)
		}
	}
}

func TestFolders(t *testing.T) {
	h := hosttest.New(t)
	repo(t, h)
	for _, d := range []string{"src/other", "src/.hidden", "node_modules/pkg/.git", "deep/a/b/c/d/.git", "notes/x"} {
		if err := os.MkdirAll(filepath.Join(h.Home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(h.Home, "src", "file.txt"), "")
	f := host.Folders("~/sr", 0)
	if !reflect.DeepEqual(f.Matches, []string{"~/src/"}) {
		t.Errorf("matches for ~/sr = %q", f.Matches)
	}
	if !reflect.DeepEqual(f.Repos, []string{"~/src/app"}) {
		t.Errorf("repos = %q: node_modules and folders too deep are skipped", f.Repos)
	}
	if f := host.Folders("~/src/", 1); len(f.Matches) != 1 {
		t.Errorf("limit 1 gave %q", f.Matches)
	}
	if f := host.Folders("~/src/", 0); !reflect.DeepEqual(f.Matches, []string{"~/src/app/", "~/src/other/"}) {
		t.Errorf("matches for ~/src/ = %q: files and hidden folders are left out", f.Matches)
	}
	if f := host.Folders("~/src/.h", 0); !reflect.DeepEqual(f.Matches, []string{"~/src/.hidden/"}) {
		t.Errorf("a hidden folder asked for by name: %q", f.Matches)
	}
	start(t, "in-notes", "sleep", "60")
	if f := host.Folders("/no/such/place/", 0); len(f.Matches) != 0 || !reflect.DeepEqual(f.Recent, []string{"~"}) {
		t.Errorf("folders = %+v", f)
	}
}

// An agent that is not installed is found before the worktree is made, so a
// failed start leaves no worktree and no branch behind.
func TestNewWithAMissingAgentMakesNoWorktree(t *testing.T) {
	h := hosttest.New(t)
	dir := repo(t, h)
	_, err := host.New(host.NewArgs{Cwd: "~/src/app", Worktree: true, Argv: []string{"no-such-program-here"}})
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != "agent_missing" {
		t.Fatalf("new = %v, want agent_missing", err)
	}
	if list := git(t, dir, "worktree", "list", "--porcelain"); strings.Count(list, "worktree ") != 1 {
		t.Errorf("a worktree was left behind:\n%s", list)
	}
	if branches := git(t, dir, "branch", "--list", brand.Name+"/*"); strings.TrimSpace(branches) != "" {
		t.Errorf("a branch was left behind: %s", branches)
	}
}
