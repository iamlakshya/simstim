// Package api holds the types the client and the host helper exchange.
// It has no dependencies, so the client stays small and portable.
package api

import (
	"fmt"
	"time"
)

// Session states.
const (
	StateStarting   = "starting"
	StateWorking    = "working"
	StateIdle       = "idle"
	StateNeedsInput = "needs_input"
	StateExited     = "exited"
	StateErrored    = "errored"
	StateLost       = "lost"    // the holder is gone, for example after a reboot
	StateStopped    = "stopped" // stopped by request; kept state stays for resume or restore
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
	// SetupSkip, or "" to follow the user's answers for this host by
	// repository (host/setup.go).
	Setup string `json:"setup,omitempty"`
	// SetupTrust holds the user's refusals, as false: those repositories'
	// scripts do not run. An older client also sends true for a yes, which
	// a helper now ignores.
	SetupTrust map[string]bool `json:"setup_trust,omitempty"`
	// SetupHashes holds, by repository, the setup hash that the user
	// trusted. The scripts run only while their content has that hash.
	SetupHashes map[string]string `json:"setup_hashes,omitempty"`
	// Wall names the isolation wall for this session: "", "none", or a driver
	// such as "plain" (the test-only supervisor driver).
	Wall string `json:"wall,omitempty"`
	// PackageManagers picks the guest package manager guard level ("guarded",
	// "strict", or "off") for walled sessions (isolation spec 7.10).
	PackageManagers string `json:"package_managers,omitempty"`
	// MinReleaseAge sets the minimum package release age ("0d" to "365d") for
	// walled sessions (isolation spec 7.10).
	MinReleaseAge string `json:"min_release_age,omitempty"`
	// SandboxEnv sets extra environment variables for walled sessions; any
	// name here overrides values written by guest init.
	SandboxEnv map[string]string `json:"sandbox_env,omitempty"`
}

// WallInfo is a session's wall, in its row.
type WallInfo struct {
	Level           string `json:"level"`
	Driver          string `json:"driver"`
	Version         string `json:"version,omitempty"`
	Floor           string `json:"floor,omitempty"`
	DowngradedFrom  string `json:"downgraded_from,omitempty"`
	Preset          string `json:"preset,omitempty"`
	PackageManagers string `json:"package_managers,omitempty"`
	MinReleaseAge   string `json:"min_release_age,omitempty"`
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
	// Wall describes the sandbox wall of a walled session, or nil for an
	// unwalled session.
	Wall *WallInfo `json:"wall,omitempty"`
	// Claimed is true when State, Source and Reason come from the guest
	// inside the wall rather than from the trusted side.
	Claimed bool `json:"claimed,omitempty"`
	// Ports lists the guest ports currently granted for forwarding on a walled
	// session (spec 7.4).
	Ports []int `json:"ports,omitempty"`
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

// StopArgs asks a host to stop a session and keep its state on disk for a
// later resume or restore: the "stop" request. A helper that can do it
// offers the feature "stop".
type StopArgs struct {
	ID string `json:"id"`
}

// StopResult is what a stop did: the session's updated row, and whether
// the session was already stopped.
type StopResult struct {
	Session        SessionInfo `json:"session"`
	AlreadyStopped bool        `json:"already_stopped,omitempty"`
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
	// AgentVersions is each agent's version by kind, as its `--version`
	// says. An agent that gives none is missing, and so is the shell. An
	// older helper sends no versions.
	AgentVersions map[string]string `json:"agent_versions,omitempty"`
	// AgentChecks come from a request of their own ("agents.check"), which
	// is slower: the client adds them for `host doctor`.
	AgentChecks []AgentCheck `json:"agent_checks,omitempty"`
	// Walls holds each wall driver's probe on this host (isolation spec 10.6).
	Walls []WallProbe `json:"walls,omitempty"`
	// Sandbox holds the full sandbox probe, added by `host doctor --json`
	// (isolation spec 14.3).
	Sandbox *CapProbe `json:"sandbox,omitempty"`
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
	ID     string `json:"id"`
	Name   string `json:"name"`
	Action string `json:"action"` // restored, skipped, failed
	Why    string `json:"why,omitempty"`
	// Hint is what to try next, after a failure that has one.
	Hint string   `json:"hint,omitempty"`
	Argv []string `json:"argv,omitempty"`
}

// Text is the result as `host restore` and the boot-time restore print
// it: one line, and the hint on a line of its own.
func (r RestoreResult) Text() string {
	s := fmt.Sprintf("%-10s %-20s %-9s %s\n", r.ID, r.Name, r.Action, r.Why)
	if r.Hint != "" {
		s += "    Try: " + r.Hint + "\n"
	}
	return s
}

// Port is a TCP port a session's processes listen on.
type Port struct {
	Port    int    `json:"port"`
	Address string `json:"address"`
	PID     int    `json:"pid"`
	Process string `json:"process,omitempty"`
	// Claimed is true when the port was reported by a guest inside a wall
	// (spec 7.4).
	Claimed bool `json:"claimed,omitempty"`
	// Granted is true when the supervisor currently exposes this port on a
	// private Unix socket on the host.
	Granted bool `json:"granted,omitempty"`
	// Socket is the host-side Unix socket path while Granted is true.
	Socket string `json:"socket,omitempty"`
}

// PortGrantArgs asks the host to grant or revoke forwarding for one port of a
// walled session ("port.grant" and "port.revoke").
type PortGrantArgs struct {
	ID   string `json:"id"`
	Port int    `json:"port"`
}

// PortGrantResult is the answer to "port.grant": the host-side Unix socket
// that ssh -L connects to for a walled session.
type PortGrantResult struct {
	Port           int    `json:"port"`
	Socket         string `json:"socket"`
	AlreadyGranted bool   `json:"already_granted,omitempty"`
	// FirstForward is true on the first port grant of a walled session's run,
	// so the client can display the browser localhost cookie notice once.
	FirstForward bool `json:"first_forward,omitempty"`
}

// PortRevokeResult is the answer to "port.revoke".
type PortRevokeResult struct {
	Port    int  `json:"port"`
	Revoked bool `json:"revoked"`
}

// DeleteResult says what deleting a session did.
type DeleteResult struct {
	Deleted  bool   `json:"deleted"`
	Worktree string `json:"worktree,omitempty"`
	// WorktreeCode and WorktreeHint come with a Worktree note when an error
	// kept the worktree, such as git_scope: the error's code, and what to
	// try. An older helper sends neither.
	WorktreeCode string `json:"worktree_code,omitempty"`
	WorktreeHint string `json:"worktree_hint,omitempty"`
}

// FoldersResult is what the new-session dialog completes folders from.
type FoldersResult struct {
	Matches []string `json:"matches"`
	Recent  []string `json:"recent"`
	Repos   []string `json:"repos"`
}
