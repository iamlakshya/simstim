// Package api holds the types the client and the host helper exchange.
// It has no dependencies, so the client stays small and portable.
package api

import "time"

// Session states.
const (
	StateStarting   = "starting"
	StateWorking    = "working"
	StateIdle       = "idle"
	StateNeedsInput = "needs_input"
	StateExited     = "exited"
	StateErrored    = "errored"
	StateLost       = "lost" // the holder is gone, for example after a reboot
)

// ReasonChildOOM is a session's reason after the memory cap stopped a
// process that the agent started, not the agent itself. The client reads
// it to tell that kill from how the agent ended.
const ReasonChildOOM = "a process in this session ran out of memory and was stopped"

// NewArgs describes a session to start.
type NewArgs struct {
	Name  string   `json:"name,omitempty"`
	Agent string   `json:"agent,omitempty"`
	Argv  []string `json:"argv,omitempty"`
	Cwd   string   `json:"cwd,omitempty"`
	// MakeFolder makes Cwd, and any missing folders above it, when Cwd does
	// not exist and is a full path. Without it, a missing Cwd is a
	// CodeNoFolder error. A helper that can do it offers the feature "mkdir".
	MakeFolder         bool   `json:"make_folder,omitempty"`
	Cols               int    `json:"cols,omitempty"`
	Rows               int    `json:"rows,omitempty"`
	Term               string `json:"term,omitempty"`
	ColorTerm          string `json:"colorterm,omitempty"`
	TermProgram        string `json:"term_program,omitempty"`
	TermProgramVersion string `json:"term_program_version,omitempty"`
	// MemoryMax caps the session's memory, in bytes. 0 asks for the host's
	// default. A helper refuses less than MinMemoryMax, and lowers more than
	// its limit for all sessions together to that limit (decision 0024).
	MemoryMax int64 `json:"memory_max,omitempty"`
	// Worktree gives the session its own git worktree and branch, from Base (default HEAD).
	Worktree bool   `json:"worktree,omitempty"`
	Base     string `json:"base,omitempty"`
	// Inline starts the agent in its inline mode, when it has one.
	Inline bool `json:"inline,omitempty"`
	// Secrets go into the agent's environment only. The client fetches them
	// for this start; the host never writes them down (host/secrets.go).
	Secrets map[string]Secret `json:"secrets,omitempty"`
	// Setup says whether the repository's setup script runs: SetupRun,
	// SetupSkip, or "" to follow SetupTrust, the user's answers for this
	// host by repository (host/setup.go).
	Setup      string          `json:"setup,omitempty"`
	SetupTrust map[string]bool `json:"setup_trust,omitempty"`
}

// SessionInfo is one row of the session list.
type SessionInfo struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Agent          string    `json:"agent"`
	Cwd            string    `json:"cwd"`
	State          string    `json:"state"`
	Source         string    `json:"source,omitempty"` // what the state was learnt from
	Since          time.Time `json:"since"`
	Reason         string    `json:"reason,omitempty"`
	ExitCode       *int      `json:"exit_code,omitempty"`
	Signal         string    `json:"signal,omitempty"`
	OOM            bool      `json:"oom_killed,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	LastActivityAt time.Time `json:"last_activity_at,omitempty"`
	LastAttachedAt time.Time `json:"last_attached_at,omitempty"`
	Alive          bool      `json:"alive"`
	Unit           string    `json:"unit,omitempty"`
	MemoryMax      int64     `json:"memory_max,omitempty"`
	MemoryCurrent  int64     `json:"memory_current,omitempty"`
	HostVersion    string    `json:"host_version"`
	Cols           int       `json:"cols"`
	Rows           int       `json:"rows"`
	// FullScreen is true while the program draws on the alternate screen,
	// so the terminal keeps no scrollback of it.
	FullScreen bool `json:"full_screen,omitempty"`
	// MissingSecrets names the secrets the session asked for but started
	// without, for example after a host restart.
	MissingSecrets []string `json:"missing_secrets,omitempty"`
	// Secrets names the secrets the session asked for at its start, never
	// their values. A resume fetches them again.
	Secrets []string `json:"secrets,omitempty"`
	// DebugUntil is when the session's debug stops by itself, while
	// `debug start` has it on.
	DebugUntil *time.Time `json:"debug_until,omitempty"`
}

// ResumeArgs asks a host to start the agent of a session again, in the same
// session, after the agent ended: the "resume" request. A helper that can
// do it offers the feature "resume".
type ResumeArgs struct {
	ID string `json:"id"`
	// MemoryMax is the session's memory cap from now on, in bytes. 0 keeps
	// the size that the session asked for before, and MemoryAuto asks for
	// the host's default.
	MemoryMax int64 `json:"memory_max,omitempty"`
	// Secrets are the values of the session's secrets, which the client
	// fetches again. The host writes none of them down (host/secrets.go).
	Secrets map[string]Secret `json:"secrets,omitempty"`
}

// ResumeResult is what a resume did: the session's new row, and how its
// agent came back, such as "resumed its own session …".
type ResumeResult struct {
	Session SessionInfo `json:"session"`
	Why     string      `json:"why"`
	// DebugStopped is true when `debug start` had the session's debug on.
	// It stopped with the old holder: a new holder starts with debug off.
	DebugStopped bool `json:"debug_stopped,omitempty"`
}

// Check is one line of the host doctor.
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
	Fix    string `json:"fix,omitempty"`
}

// HostInfo describes the host.
type HostInfo struct {
	Hostname  string            `json:"hostname"`
	OS        string            `json:"os"`
	Arch      string            `json:"arch"`
	Kernel    string            `json:"kernel,omitempty"`
	Distro    string            `json:"distro,omitempty"`
	CPUs      int               `json:"cpus"`
	MemTotal  int64             `json:"mem_total"`
	DiskFree  int64             `json:"disk_free"`
	Version   string            `json:"version"`
	User      string            `json:"user"`
	Agents    map[string]string `json:"agents"`
	Checks    []Check           `json:"checks"`
	MemoryMax int64             `json:"memory_max"`
	// AgentChecks come from a request of their own ("agents.check"), which
	// is slower: the client adds them for `host doctor`.
	AgentChecks []AgentCheck `json:"agent_checks,omitempty"`
}

// SetupArgs asks the host to prepare itself.
type SetupArgs struct {
	Linger   bool              `json:"linger"`
	Terminfo map[string]string `json:"terminfo,omitempty"`
}

// SetupResult reports what setup did.
type SetupResult struct {
	Steps []Check `json:"steps"`
}

// RestoreResult is one session that restore looked at.
type RestoreResult struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Action string   `json:"action"` // restored, skipped, failed
	Why    string   `json:"why,omitempty"`
	Argv   []string `json:"argv,omitempty"`
}

// Port is a TCP port a session's processes listen on.
type Port struct {
	Port    int    `json:"port"`
	Address string `json:"address"`
	PID     int    `json:"pid"`
	Process string `json:"process,omitempty"`
}

// DeleteResult says what deleting a session did.
type DeleteResult struct {
	Deleted  bool   `json:"deleted"`
	Worktree string `json:"worktree,omitempty"`
}

// FoldersResult is what the new-session dialog completes folders from.
type FoldersResult struct {
	Matches []string `json:"matches"`
	Recent  []string `json:"recent"`
	Repos   []string `json:"repos"`
}
