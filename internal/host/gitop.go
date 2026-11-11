package host

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/proto"
)

// Git on the trusted side (spec 19.3). The helper runs git in folders that
// an unwalled session can write: its worktree, and the main repository's
// .git, which the worktree shares. A repository's config can name commands
// that git runs, such as a clean filter, and no option turns all of them
// off. So each operation on a repository runs in a child of its own,
// `<helper> git-op <op>`, with a JSON request on stdin and a JSON answer on
// stdout. With a systemd user manager, the child runs in a scope under the
// sessions' slice (gitscope.go). git, and every command that git starts,
// then sit with the sessions' own processes, where the holders' peer check
// holds them.

// The git operations.
const (
	gitOpWorktreeAdd     = "worktree.add"
	gitOpWorktreeRemove  = "worktree.remove"
	gitOpWorktreeDiscard = "worktree.discard"
	gitOpRepoInfo        = api.GitOpRepoInfo
)

// gitOpKind is a git operation: its name, how long it may run, and what
// runs it in the child. The deadlines are project choices. worktree.add
// gets a long one, because a checkout can download large files, as Git LFS
// does.
type gitOpKind struct {
	name     string
	deadline time.Duration
	// run runs the operation. plan, when it is not nil, gets the worktree
	// that worktree.add is about to make, before the checkout starts.
	run func(r gitOpRequest, plan func(*GitInfo)) (gitOpAnswer, error)
}

// gitOpKinds are the git operations, in the order that the usage lists
// them. Tests shorten the deadlines. Each operation passes the version gate
// before its first git command that can fire a hook: repo.info only when it
// counts the changed files, which runs git status.
var gitOpKinds = []*gitOpKind{
	{gitOpWorktreeAdd, 10 * time.Minute, func(r gitOpRequest, plan func(*GitInfo)) (a gitOpAnswer, err error) {
		a.Git, err = addWorktree(r.Add, plan)
		return a, err
	}},
	{gitOpWorktreeRemove, time.Minute, func(r gitOpRequest, _ func(*GitInfo)) (gitOpAnswer, error) {
		if err := gitVersionGate(); err != nil {
			return gitOpAnswer{}, err
		}
		res, err := removeWorktreeHere(r.Git)
		return gitOpAnswer{Result: res}, err
	}},
	{gitOpWorktreeDiscard, time.Minute, func(r gitOpRequest, _ func(*GitInfo)) (gitOpAnswer, error) {
		if err := gitVersionGate(); err != nil {
			return gitOpAnswer{}, err
		}
		return gitOpAnswer{}, discardWorktreeHere(r.Git)
	}},
	{gitOpRepoInfo, time.Minute, func(r gitOpRequest, _ func(*GitInfo)) (a gitOpAnswer, err error) {
		a.Repo, err = repoInfo(r.Dir, r.Changes)
		return a, err
	}},
}

// findGitOp is the git operation called name, or nil.
func findGitOp(name string) *gitOpKind {
	for _, k := range gitOpKinds {
		if k.name == name {
			return k
		}
	}
	return nil
}

// gitOpWaitDelay is how long a git operation's child may hold its output
// open after it was stopped.
const gitOpWaitDelay = 5 * time.Second

// worktreeAddArgs makes a new worktree for a session.
type worktreeAddArgs struct {
	Dir  string `json:"dir"`  // the session's folder, in the repository
	Name string `json:"name"` // the session's name, for the branch and folder
	ID   string `json:"id"`   // the session's ID
	Base string `json:"base"` // the commit to start from, or "" for HEAD
}

// gitOpRequest is a git operation's request.
type gitOpRequest struct {
	// RepoInfoArgs is repo.info's request, the one that the client sends.
	api.RepoInfoArgs
	// Add is worktree.add's request.
	Add *worktreeAddArgs `json:"add,omitempty"`
	// Git is the worktree that worktree.remove and worktree.discard act on.
	Git *GitInfo `json:"git,omitempty"`
	// DeadlineMS is the operation's deadline, in milliseconds, which the
	// child keeps itself. It may only shorten the operation's own.
	DeadlineMS int64 `json:"deadline_ms,omitempty"`
}

// deadlineFor is the deadline of the operation for req: its own, or the
// request's, when that is shorter.
func (k *gitOpKind) deadlineFor(req gitOpRequest) time.Duration {
	if d := time.Duration(req.DeadlineMS) * time.Millisecond; d > 0 && d < k.deadline {
		return d
	}
	return k.deadline
}

// gitOpAnswer is a git operation's answer. The child of worktree.add first
// writes one with Plan alone, before the checkout, and then the answer.
type gitOpAnswer struct {
	Error  *proto.Error  `json:"error,omitempty"`
	Git    *GitInfo      `json:"git,omitempty"`    // worktree.add
	Repo   *api.RepoInfo `json:"repo,omitempty"`   // repo.info
	Result string        `json:"result,omitempty"` // worktree.remove: what it did
	Plan   *GitInfo      `json:"plan,omitempty"`   // worktree.add: what it is about to make
}

// gitOpHint is the next step after a git operation that failed on the host.
var gitOpHint = "Run `" + brand.Name + " host doctor` to check the host."

// RunGitOp is the helper's git-op command. It reads the request of the git
// operation args[0] on stdin, and writes the answer on stdout. A child that
// gitOp started runs the operation itself, within its deadline. Started any
// other way, as the client does over SSH, git-op starts that child, in its
// scope, first.
func RunGitOp(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) != 1 || findGitOp(args[0]) == nil {
		names := make([]string, len(gitOpKinds))
		for i, k := range gitOpKinds {
			names[i] = k.name
		}
		return UserError("usage", "usage: "+brand.HostBinary+" git-op <"+strings.Join(names, "|")+">",
			"Send the request as one JSON object on stdin.")
	}
	op := args[0]
	var req gitOpRequest
	if err := json.NewDecoder(stdin).Decode(&req); err != nil {
		return UserError("bad_request", "git-op "+op+" could not read its request: "+err.Error(),
			"Send the request as one JSON object on stdin.")
	}
	enc := json.NewEncoder(stdout)
	if os.Getenv(gitScopeEnv) != "" {
		return runGitOpChild(findGitOp(op), req, enc)
	}
	ans, err := gitOp(op, req)
	ans.Plan = nil
	if err != nil {
		ans.Error = asProtoError(err)
	}
	return writeGitOpAnswer(enc, op, ans)
}

// repoInfo is repo.info, in the git operation's child: the top folder of
// dir's work tree, its main repository's folder, its branch and, when
// changes asks, the count of its changed files.
func repoInfo(dir string, changes bool) (*api.RepoInfo, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, UserError("no_git", "git is not installed on this host", "Install git on the host.")
	}
	top, err := git(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, notARepo(dir, err)
	}
	common, err := git(dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return nil, notARepo(dir, err)
	}
	r := &api.RepoInfo{Root: top, Main: mainFolder(dir, common), Branch: "HEAD"}
	// symbolic-ref names the branch of a repository without commits too.
	if b, err := git(dir, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil && b != "" {
		r.Branch = b
	}
	if changes {
		// git status can write the index, which fires a hook.
		if err := gitVersionGate(); err != nil {
			return nil, err
		}
		st, err := git(dir, "status", "--porcelain")
		if err != nil {
			return nil, asUserError(err, "git", "Check the repository's state on the host with git status.")
		}
		r.Dirty = countLines(st)
	}
	return r, nil
}

// notARepo is the error of a folder outside git. An error of git_hooks
// stays itself.
func notARepo(dir string, err error) error {
	var pe *proto.Error
	if errors.As(err, &pe) {
		return pe
	}
	return UserError("not_a_repo", dir+" is not inside a git repository", "Pick a folder inside a repository, or start without a worktree.")
}

// mainFolder is the main repository's folder, from git's common folder as
// `rev-parse --git-common-dir` printed it in dir. Every worktree of a
// repository has the same one. A bare repository's is the common folder.
func mainFolder(dir, common string) string {
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	common = filepath.Clean(common)
	if filepath.Base(common) == ".git" {
		return filepath.Dir(common)
	}
	return common
}

// asUserError is err with a code and a hint: its own, when it has them.
func asUserError(err error, code, hint string) *proto.Error {
	var pe *proto.Error
	if errors.As(err, &pe) {
		return pe
	}
	return UserError(code, err.Error(), hint)
}

// asProtoError is err as the protocol's error, for the answer of git-op.
func asProtoError(err error) *proto.Error {
	return asUserError(err, "git", gitOpHint)
}
