package api

// Setup values in NewArgs: run the repository's setup script, or skip it.
const (
	SetupRun  = "run"
	SetupSkip = "skip"
)

// CodeSetupTrust is the error code of a new session whose repository has a
// setup script that the user has neither refused on this host nor trusted
// with its current content. The error's data is a SetupQuestion.
const CodeSetupTrust = "setup_trust"

// CodeSetupOutside is the error code of a new session whose repository's
// setup or resume script leaves the repository through a symlink, so it
// cannot run.
const CodeSetupOutside = "setup_outside"

// CodeSetupUnreadable is the error code of a new session whose repository's
// setup, resume or stop script is executable but cannot be read, so its
// content cannot be checked.
const CodeSetupUnreadable = "setup_unreadable"

// SetupQuestion asks the user whether a repository's setup script may run.
type SetupQuestion struct {
	// Repo is the repository on the host: its main folder, which its
	// worktrees share. The answer is kept by host and Repo.
	Repo string `json:"repo"`
	// Script is the script that would run.
	Script string `json:"script"`
	// Hash is the setup hash of the repository's scripts: a yes holds for
	// this content only. The client starts again with it in
	// NewArgs.SetupHashes. An older helper sends none.
	Hash string `json:"hash,omitempty"`
	// Kept says where a worktree that this start made, and could not
	// remove before it asked, stays on the host, and how to remove it.
	Kept string `json:"kept,omitempty"`
}
