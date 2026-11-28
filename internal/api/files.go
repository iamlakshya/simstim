package api

import (
	"fmt"
	"strings"
)

// Feature flags advertised in the bridge hello for files, editors and bundle pull.
const (
	FeatureSFTP       = "sftp"
	FeatureSSHExec    = "ssh-exec"
	FeaturePullBundle = "pull.bundle"
	FeatureSeed       = "seed"
	FeaturePulled     = "pulled"
)

// Defaults and hints for sandbox.pull_max (isolation spec 6.10 and 8.1).
const (
	DefaultPullMax      = "512M"
	DefaultPullMaxBytes = 512 << 20
	PullMaxHint         = `Set sandbox.pull_max to a size such as "512M" or "1G" in config.toml`
)

// PullMaxMessage returns the error sentence for an invalid sandbox.pull_max
// value, without a trailing Try: hint.
func PullMaxMessage(raw string) string {
	return fmt.Sprintf(`sandbox.pull_max is %q, and it must be a size such as "512M" or "1G".`, raw)
}

// ParsePullMax validates sandbox.pull_max (empty defaults to 512 MiB).
func ParsePullMax(raw string) (bytes int64, canonical string, err error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return DefaultPullMaxBytes, DefaultPullMax, nil
	}
	n, perr := ParseSize(trimmed)
	if perr != nil {
		return 0, "", fmt.Errorf("%s Try: %s", PullMaxMessage(raw), PullMaxHint)
	}
	return n, trimmed, nil
}

// GitOpBundleCreate is the host helper's git operation that creates a git
// bundle for a session's branch inside the git scope, with hooks and
// fsmonitor off.
const GitOpBundleCreate = "bundle.create"

// GitOpSeedClone is the host helper's git operation that clones a staged seed
// bundle into a session's workspace inside the git scope, with hooks and
// fsmonitor off.
const GitOpSeedClone = "seed.clone"

// SSHExecArgs starts an exec command or ephemeral shell over the helper's
// ssh-exec command.
type SSHExecArgs struct {
	Command string `json:"command,omitempty"`
	PTY     bool   `json:"pty,omitempty"`
	Term    string `json:"term,omitempty"`
	Cols    int    `json:"cols,omitempty"`
	Rows    int    `json:"rows,omitempty"`
}

// SessionStreamArgs identifies a walled session for a streaming bridge op
// (such as "sftp", spec 10.5).
type SessionStreamArgs struct {
	ID string `json:"id"`
}

// SSHExecStreamArgs starts an ssh-proxy exec or shell stream inside a walled
// session through the supervisor (spec 10.5, 17.7).
type SSHExecStreamArgs struct {
	ID   string      `json:"id"`
	Exec SSHExecArgs `json:"exec"`
}

// SSHSignalEvent delivers a POSIX signal name (such as "INT" or "TERM") to a
// running ssh-exec process.
type SSHSignalEvent struct {
	Signal string `json:"signal"`
}

// DialPortReply is the one-line JSON header written by dial-port before
// splicing raw TCP bytes.
type DialPortReply struct {
	OK    bool   `json:"ok,omitempty"`
	Error string `json:"error,omitempty"`
}

// PullBundleArgs asks the host helper to bundle a session's branch and stream
// it back on stdout.
type PullBundleArgs struct {
	Base     string `json:"base,omitempty"`
	Full     bool   `json:"full,omitempty"`
	MaxBytes int64  `json:"max_bytes,omitempty"`
	// Repo selects one repository by base name when a session has more than one
	// seeded or configured repository (spec 17.4).
	Repo string `json:"repo,omitempty"`
}

// CopyInSpec records one laptop-to-session file copy configured for a session
// (spec 17.3). Only the paths are stored in the manifest; the host keeps no
// copy of the laptop's bytes.
type CopyInSpec struct {
	Source string `json:"source"`
	Guest  string `json:"guest"`
}

// CopyInFile identifies one uploaded file or folder inside the session's SFTP
// staging folder and its destination path inside the session.
type CopyInFile struct {
	StageRel  string `json:"stage_rel"`
	GuestPath string `json:"guest_path"`
	IsDir     bool   `json:"is_dir,omitempty"`
}

// SeedRepoSpec describes one laptop git bundle uploaded into the session's SFTP
// staging folder before start (spec 17.4).
type SeedRepoSpec struct {
	Name      string `json:"name"`
	Branch    string `json:"branch"`
	Origin    string `json:"origin,omitempty"`
	UserName  string `json:"user_name,omitempty"`
	UserEmail string `json:"user_email,omitempty"`
	BundleRel string `json:"bundle_rel"`
}

// SeedRepoRecord records one repository cloned into a session from a laptop
// seed bundle so `pull --repo` can locate it later.
type SeedRepoRecord struct {
	Name     string `json:"name"`
	Dir      string `json:"dir"`
	Branch   string `json:"branch"`
	SeedHead string `json:"seed_head,omitempty"`
}

// SessionStartArgs finishes starting a deferred session after its copy-in files
// and seed bundles have been uploaded over SFTP (spec 17.3, 17.4).
type SessionStartArgs struct {
	ID             string                `json:"id"`
	CopyFiles      []CopyInFile          `json:"copy_files,omitempty"`
	Seeds          []SeedRepoSpec        `json:"seeds,omitempty"`
	Secrets        map[string]Secret     `json:"secrets,omitempty"`
	SecretBinds    map[string]SecretBind `json:"secret_binds,omitempty"`
	ExposedSecrets []string              `json:"exposed_secrets,omitempty"`
}

// CopyApplyArgs places staged copy-in files into an already-running session
// (for example on attach after a boot-time restore, spec 17.3).
type CopyApplyArgs struct {
	ID        string       `json:"id"`
	CopyFiles []CopyInFile `json:"copy_files,omitempty"`
}

// PullBundleHeader is the one-line JSON header written by pull-bundle before
// the raw bundle bytes (when Empty is false and Error is nil).
type PullBundleHeader struct {
	Repo  *RepoInfo `json:"repo,omitempty"`
	Head  string    `json:"head,omitempty"`
	Full  bool      `json:"full,omitempty"`
	Size  int64     `json:"size,omitempty"`
	Empty bool      `json:"empty,omitempty"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Hint    string `json:"hint,omitempty"`
	} `json:"error,omitempty"`
}

// PulledArgs records commit OIDs successfully pulled from a session into a
// local clone (isolation spec 6.10).
type PulledArgs struct {
	ID      string   `json:"id"`
	Commits []string `json:"commits"`
}
