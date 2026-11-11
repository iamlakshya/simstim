package host

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// GitInfo is the git part of a manifest, for a session that has its own worktree.
type GitInfo struct {
	RepoRoot  string `json:"repo_root"`
	Worktree  string `json:"worktree"`
	Branch    string `json:"branch"`
	BaseRef   string `json:"base_ref"`
	BaseSHA   string `json:"base_sha"`
	CreatedBy bool   `json:"created_by_us"`

	// skipped are the entries of .worktreeinclude that a new worktree did
	// not get, and why. The start writes them to the session's log.
	skipped []skippedCopy
}

var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

// Slug turns a name into a branch-safe slug.
func Slug(s string) string {
	s = strings.Trim(slugRE.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	if s == "" {
		s = "task"
	}
	return s
}

// WorktreesDir holds the worktrees of all sessions.
func WorktreesDir() string { return filepath.Join(paths.DataDir(), "worktrees") }

// makeWorktree creates a git worktree for a new session: a new branch from base,
// in a folder of its own, locked while the session lives. git runs in a git
// operation's child. The copy of .worktreeinclude runs no git, so it runs
// here. When the add fails after it began, as when it runs past its
// deadline or StopGitOps stops it, makeWorktree removes what the add had
// begun, or says where it stays.
func makeWorktree(cwd, name, id, base string) (*GitInfo, error) {
	ans, err := gitOp(gitOpWorktreeAdd, gitOpRequest{Add: &worktreeAddArgs{Dir: cwd, Name: name, ID: id, Base: base}})
	if err != nil && ans.Plan != nil {
		return nil, rollBackWorktree(ans.Plan, err)
	}
	if err != nil {
		return nil, err
	}
	if ans.Git == nil {
		return nil, UserError("worktree", "git made no worktree", "Check the repository's state with git status.")
	}
	ans.Git.skipped = copyWorktreeInclude(ans.Git.RepoRoot, ans.Git.Worktree)
	return ans.Git, nil
}

// rollBackDeadline bounds the removal of a worktree whose add failed after
// it began. A project choice: the removal checks out nothing.
var rollBackDeadline = 30 * time.Second

// rollBackWorktree removes the worktree and branch that a failed add had
// begun, through worktree.discard in a scope of its own. The plan names
// them: a branch and a folder of this session's own ID, which nothing else
// uses. It returns err, and when the removal fails, it adds where they stay.
func rollBackWorktree(plan *GitInfo, err error) error {
	if _, derr := gitOpWithin(findGitOp(gitOpWorktreeDiscard), gitOpRequest{Git: plan}, rollBackDeadline); derr != nil {
		return worktreeStays(err, plan)
	}
	return err
}

// addWorktree is worktree.add, in the git operation's child. Before the
// checkout, which runs a repository's filters, it gives plan the worktree
// that it is about to make.
func addWorktree(a *worktreeAddArgs, plan func(*GitInfo)) (*GitInfo, error) {
	if a == nil {
		return nil, UserError("bad_request", "worktree.add needs its arguments", "Send them as the request's add.")
	}
	if _, err := exec.LookPath("git"); err != nil {
		return nil, UserError("no_git", "git is not installed on this host", "Install git on the host, or start the session without a worktree.")
	}
	root, err := git(a.Dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, notARepo(a.Dir, err)
	}
	base := a.Base
	if base == "" {
		base = "HEAD"
	}
	sha, err := git(root, "rev-parse", "--verify", base+"^{commit}")
	if err != nil {
		return nil, UserError("bad_base", fmt.Sprintf("%s is not a commit in %s", base, root), "Pass an existing branch or commit as the base.")
	}
	// The checkout fires hooks, so the version gate comes before anything
	// is made.
	if err := gitVersionGate(); err != nil {
		return nil, err
	}
	slug := Slug(a.Name)
	branch := brand.Name + "/" + slug + "-" + a.ID
	wt := filepath.Join(WorktreesDir(), filepath.Base(root), slug+"-"+a.ID)
	if err := os.MkdirAll(filepath.Dir(wt), 0o700); err != nil {
		return nil, UserError("worktree", "could not make the folder for the worktree: "+err.Error(), "Check the host's disk with the host doctor.")
	}
	// When the add fails, makeWorktree removes the plan's folder and
	// branch. So both must be new: this add's own, and nothing that was
	// there before.
	if _, err := os.Lstat(wt); !errors.Is(err, fs.ErrNotExist) {
		return nil, UserError("worktree", "the folder "+wt+" for the new worktree is already there", "Start the session again: a new start picks a new ID.")
	}
	switch gone, err := branchMissing(root, branch); {
	case err != nil:
		return nil, asUserError(err, "worktree", "Check the repository's state with git status.")
	case !gone:
		return nil, UserError("worktree", fmt.Sprintf("the branch %s is already in %s", branch, root), "Start the session again: a new start picks a new ID.")
	}
	g := &GitInfo{RepoRoot: root, Worktree: wt, Branch: branch, BaseRef: base, BaseSHA: sha, CreatedBy: true}
	if plan != nil {
		plan(g)
	}
	if _, err := git(root, "worktree", "add", "-b", branch, wt, sha); err != nil {
		return nil, asUserError(err, "worktree", "Check the repository's state with git status.")
	}
	_, _ = git(root, "worktree", "lock", "--reason", brand.DisplayName+" session "+a.ID, wt)
	return g, nil
}

// removeWorktree removes a session's worktree and branch only when that loses
// nothing: no uncommitted changes and no commits beyond the base. It reports
// what it did, for the user, and the error that kept the worktree, if any,
// so that the client can name the host and give its hint. git runs in a git
// operation's child.
func removeWorktree(g *GitInfo) (string, *proto.Error) {
	if g == nil || !g.CreatedBy || !exists(g.Worktree) {
		return "", nil
	}
	ans, err := gitOp(gitOpWorktreeRemove, gitOpRequest{Git: g})
	if err != nil {
		return keptWorktree(g, err), asProtoError(err)
	}
	return ans.Result, nil
}

// keptWorktree says that the worktree g stays, because of err.
func keptWorktree(g *GitInfo, err error) string {
	return fmt.Sprintf("kept the worktree %s (branch %s): could not check it: %v", g.Worktree, g.Branch, err)
}

// removeWorktreeHere is worktree.remove, in the git operation's child. It
// returns what it did. A failure of git comes back as the error, with its
// code and hint, such as git_hooks, so that the client can show them.
func removeWorktreeHere(g *GitInfo) (string, error) {
	if g == nil || !g.CreatedBy {
		return "", nil
	}
	hint := "Check the worktree on the host with git status."
	_, _ = git(g.RepoRoot, "worktree", "unlock", g.Worktree)
	dirty, err := git(g.Worktree, "status", "--porcelain")
	if err != nil {
		return "", asUserError(err, "worktree", hint)
	}
	ahead, err := git(g.RepoRoot, "rev-list", "--count", g.BaseSHA+".."+g.Branch)
	if err != nil {
		return "", asUserError(err, "worktree", hint)
	}
	if dirty != "" || ahead != "0" {
		return fmt.Sprintf("kept the worktree %s (branch %s): it has %s and %s", g.Worktree, g.Branch,
			plural(atoi(ahead), "new commit", "new commits"), plural(countLines(dirty), "changed file", "changed files")), nil
	}
	if _, err := git(g.RepoRoot, "worktree", "remove", g.Worktree); err != nil {
		return "", asUserError(err, "worktree", hint)
	}
	_, _ = git(g.RepoRoot, "branch", "-D", g.Branch)
	return "removed the clean worktree and its branch", nil
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}
