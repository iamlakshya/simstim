package api

// Feature flags advertised in the bridge hello for files, editors and bundle pull.
const (
	FeatureSFTP       = "sftp"
	FeatureSSHExec    = "ssh-exec"
	FeaturePullBundle = "pull.bundle"
)

// GitOpBundleCreate is the host helper's git operation that creates a git
// bundle for a session's branch inside the git scope, with hooks and
// fsmonitor off.
const GitOpBundleCreate = "bundle.create"

// SSHExecArgs starts an exec command or ephemeral shell over the helper's
// ssh-exec command.
type SSHExecArgs struct {
	Command string `json:"command,omitempty"`
	PTY     bool   `json:"pty,omitempty"`
	Term    string `json:"term,omitempty"`
	Cols    int    `json:"cols,omitempty"`
	Rows    int    `json:"rows,omitempty"`
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
