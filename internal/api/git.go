package api

// GitOpRepoInfo is the host helper's git operation that describes the git
// repository of a folder. The client runs it over SSH as
// `<helper> git-op repo.info`, with a RepoInfoArgs on stdin. The helper
// writes a JSON object on stdout: "repo", a RepoInfo, or "error", the error.
const GitOpRepoInfo = "repo.info"

// The errors of the host's git operations.
const (
	// CodeGitScope is the error of a git operation that could not start in
	// a scope under the sessions' slice, although the host has a systemd
	// user manager, or whose host could not say whether it has one.
	CodeGitScope = "git_scope"
	// CodeGitHooks is the error of a git operation that could not read the
	// hooks that git's config defines, or could not turn one off.
	CodeGitHooks = "git_hooks"
	// CodeGitTimeout is the error of a git operation that ran past its
	// deadline, and was stopped.
	CodeGitTimeout = "git_timeout"
	// CodeGitVersion is the error of a git operation that the host's git
	// cannot run safely: git 2.54, whose hooks in a repository's config no
	// switch turns off, or a git whose version the helper cannot read.
	CodeGitVersion = "git_version"
)

// RepoInfoArgs asks repo.info about a folder.
type RepoInfoArgs struct {
	// Dir is the folder on the host.
	Dir string `json:"dir,omitempty"`
	// Changes asks for the count of changed files too. That runs git status,
	// which can take a while in a large repository.
	Changes bool `json:"changes,omitempty"`
}

// RepoInfo is what repo.info says about a folder's git repository.
type RepoInfo struct {
	// Root is the top folder of the folder's work tree: a worktree's own.
	Root string `json:"root"`
	// Main is the main repository's folder, from git's common folder. Every
	// worktree of a repository has the same one.
	Main string `json:"main"`
	// Branch is the branch checked out there, or "HEAD" when it is detached.
	Branch string `json:"branch"`
	// Dirty counts the files with uncommitted changes, when Changes asked.
	Dirty int `json:"dirty"`
}
