package host

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
)

// Session states, shown in the client's session list.
const (
	StateStarting   = api.StateStarting
	StateWorking    = api.StateWorking
	StateIdle       = api.StateIdle
	StateNeedsInput = api.StateNeedsInput
	StateExited     = api.StateExited
	StateErrored    = api.StateErrored
	StateLost       = api.StateLost
	StateStopped    = api.StateStopped
)

// Manifest is the record of one session, at ~/.local/state/<name>/sessions/<id>/manifest.json.
// Readers keep fields they do not know, so v2 can add fields freely.
type Manifest struct {
	Schema         int       `json:"schema"`
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	LastActivityAt time.Time `json:"last_activity_at,omitempty"`
	LastAttachedAt time.Time `json:"last_attached_at,omitempty"`
	HostVersion    string    `json:"host_version"`
	HolderBinary   string    `json:"holder_binary"`

	Agent  AgentInfo         `json:"agent"`
	Cwd    string            `json:"cwd"`
	Env    []string          `json:"env"`
	Term   TermInfo          `json:"term"`
	Limits Limits            `json:"limits"`
	Run    Runtime           `json:"runtime"`
	Status Status            `json:"status"`
	Labels map[string]string `json:"labels,omitempty"`

	// Restart is used by v2 respawn.
	Restart Restart `json:"restart"`
	// Git is set when the session has its own worktree.
	Git *GitInfo `json:"git,omitempty"`
	// SetupHook and ResumeHook run in the session's terminal before the agent starts.
	// StopHook runs before a user-requested stop tears the session's unit down.
	SetupHook  string `json:"setup_hook,omitempty"`
	ResumeHook string `json:"resume_hook,omitempty"`
	StopHook   string `json:"stop_hook,omitempty"`
	// RunHook is the hook for the next start: the setup hook, or the resume hook after a restart.
	RunHook string `json:"run_hook,omitempty"`
	// SetupHash is the setup hash of the repository's scripts at the start,
	// when they are the session's hooks. A respawn skips the repository's
	// resume script when its hash differs then (setup.go).
	SetupHash string `json:"setup_hash,omitempty"`
	// Secrets names the variables the client passed at the start. Their
	// values are never written down (secrets.go). MissingSecrets names the
	// ones this run of the agent started without.
	Secrets        []string `json:"secrets,omitempty"`
	MissingSecrets []string `json:"missing_secrets,omitempty"`
	// DebugUntil is when the session's debug stops by itself, while
	// `debug start` has it on (holder_debug.go).
	DebugUntil *time.Time `json:"debug_until,omitempty"`
	// Wall is set for sessions that run under a supervisor and guest init.
	Wall *WallRecord `json:"wall,omitempty"`
	// Guest holds the latest claims reported by the guest inside the wall.
	// Every string is cleaned with api.CleanText before it is kept.
	Guest *GuestClaims `json:"guest,omitempty"`
	// Image records the session's microvm image, when it boots from one.
	Image *ManifestImage `json:"image,omitempty"`
	// Agents is the SHA-256 digest of the agents disk this session uses.
	Agents string `json:"agents,omitempty"`
	// GrantedPorts records guest ports currently granted on a walled session's
	// supervisor (spec 7.4).
	GrantedPorts []int `json:"granted_ports,omitempty"`
	// Usage records cumulative model token usage and budget for the session
	// (isolation spec 17.8).
	Usage *api.SessionUsage `json:"usage,omitempty"`
	// HostName is the client's saved name for this host (spec 17.2).
	HostName string `json:"host_name,omitempty"`
	// SandboxName is the session's initial name, kept for the SANDBOX identity
	// environment variable even after a rename (spec 17.2).
	SandboxName string `json:"sandbox_name,omitempty"`
	// CopyIn holds the laptop-to-session file copy paths configured at start.
	// The host never stores file contents; CopiesPending marks a restored
	// session whose files wait for the next client attach (spec 17.3).
	CopyIn        []api.CopyInSpec     `json:"copy_in,omitempty"`
	CopiesPending bool                 `json:"copies_pending,omitempty"`
	SeedRepos     []api.SeedRepoRecord `json:"seed_repos,omitempty"`

	extra map[string]json.RawMessage
}

// WallMount records one extra bind mount for a walled session.
type WallMount struct {
	Source   string `json:"source"`
	Dest     string `json:"dest"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

// WallRecord records the wall a session runs in, written by the trusted side.
type WallRecord struct {
	Level           string            `json:"level"`
	Driver          string            `json:"driver"`
	Version         string            `json:"version,omitempty"`
	Floor           string            `json:"floor,omitempty"`
	DowngradedFrom  string            `json:"downgraded_from,omitempty"`
	Preset          string            `json:"preset,omitempty"`
	Reason          string            `json:"reason,omitempty"`
	At              time.Time         `json:"at,omitempty"`
	WorkspaceHost   string            `json:"workspace_host,omitempty"`
	WorkspaceGuest  string            `json:"workspace_guest,omitempty"`
	StateDirs       []string          `json:"state_dirs,omitempty"`
	CacheDirs       []string          `json:"cache_dirs,omitempty"`
	ExtraRO         []WallMount       `json:"extra_ro,omitempty"`
	ExtraRW         []WallMount       `json:"extra_rw,omitempty"`
	ShareNet        bool              `json:"share_net,omitempty"`
	PolicyTOML      string            `json:"policy_toml,omitempty"`
	PackageManagers string            `json:"package_managers,omitempty"`
	MinReleaseAge   string            `json:"min_release_age,omitempty"`
	ModelBudget     int64             `json:"model_budget,omitempty"`
	UploadBudget    string            `json:"upload_budget,omitempty"`
	SandboxEnv      map[string]string `json:"sandbox_env,omitempty"`
	// BoundSecrets records destination-binding metadata for secrets held only
	// on the trusted egress gateway. Values are never stored here.
	BoundSecrets []api.SecretBind `json:"bound_secrets,omitempty"`
	// ExposedSecrets names secrets explicitly allowed into the sandbox env.
	ExposedSecrets []string `json:"exposed_secrets,omitempty"`
	Runtime        string   `json:"runtime,omitempty"`
	RuntimeFiles   []string `json:"runtime_files,omitempty"`
}

// GuestClaims holds the latest reports from the guest inside the wall.
type GuestClaims struct {
	State      string     `json:"state,omitempty"`
	Source     string     `json:"source,omitempty"`
	Reason     string     `json:"reason,omitempty"`
	FullScreen bool       `json:"full_screen,omitempty"`
	Since      time.Time  `json:"since,omitempty"`
	Ports      []api.Port `json:"ports,omitempty"`
	UpdatedAt  time.Time  `json:"updated_at,omitempty"`
}

// ManifestImage records the OCI image digest and dm-verity root hash used by a
// microvm session (spec 10.7).
type ManifestImage struct {
	Ref            string `json:"ref,omitempty"`
	Digest         string `json:"digest,omitempty"`
	PlatformDigest string `json:"platform_digest,omitempty"`
	RootHash       string `json:"root_hash,omitempty"`
}

// AgentInfo is the agent part of a manifest.
type AgentInfo struct {
	Kind string `json:"kind"`
	// Argv is the command of the agent's latest start. After a restore or a
	// resume, it is the command that resumeArgv picked.
	Argv []string `json:"argv"`
	// Command is the command that the session started with, when the user
	// gave it rather than the agent's launch command. A restore or a resume
	// runs it again, unless the agent resumes its own session by ID. An
	// older manifest has none.
	Command        []string  `json:"command,omitempty"`
	ResumeArgv     []string  `json:"resume_argv,omitempty"`
	ContinueArgv   []string  `json:"continue_argv,omitempty"`
	AgentSessionID string    `json:"agent_session_id,omitempty"`
	IDSource       string    `json:"id_source,omitempty"`
	IDHistory      []IDEvent `json:"id_history,omitempty"`
	// Hooks are the extra arguments that report status; kept so a respawn adds them again.
	Hooks []string `json:"hooks,omitempty"`
	// Inline are the arguments that start the agent in its inline mode, when
	// the session asked for it; kept so a respawn adds them again.
	Inline []string `json:"inline,omitempty"`
}

// IDEvent records a change of the agent's own session ID, for example after /clear.
type IDEvent struct {
	ID     string    `json:"id"`
	Source string    `json:"source,omitempty"`
	At     time.Time `json:"at"`
}

// TermInfo is the terminal identity given to the agent.
type TermInfo struct {
	Term string `json:"TERM"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

// Limits are the resource caps of the session.
type Limits struct {
	MemoryHigh int64 `json:"memory_high,omitempty"`
	// MemoryMax is the memory cap that the session's unit got at its last
	// start, in bytes.
	MemoryMax int64 `json:"memory_max,omitempty"`
	// MemoryAsked is the cap that the user asked for, or 0 for the host's
	// default. Each start works MemoryMax out from it again, so a session
	// that restore starts after a reboot gets the default of that time.
	MemoryAsked int64 `json:"memory_asked,omitempty"`
	// CPUs is the CPU cap that the session's unit got at its last start (as
	// systemd CPUQuota=<CPUs*100>%).
	CPUs int `json:"cpus,omitempty"`
	// CPUsAsked is the CPU cap that the user asked for, or 0 for default.
	CPUsAsked int `json:"cpus_asked,omitempty"`
	// DiskMax is the soft disk cap for a walled session's folder, in bytes.
	DiskMax int64 `json:"disk_max,omitempty"`
}

// Runtime is live process information.
type Runtime struct {
	HolderPID    int       `json:"holder_pid,omitempty"`
	AgentPID     int       `json:"agent_pid,omitempty"`
	Unit         string    `json:"unit,omitempty"`
	Socket       string    `json:"socket"`
	BootID       string    `json:"boot_id,omitempty"`
	StartedAt    time.Time `json:"started_at,omitempty"`
	RespawnCount int       `json:"respawn_count"`
}

// Status is the session's state and why.
type Status struct {
	State    string    `json:"state"`
	Since    time.Time `json:"since"`
	Source   string    `json:"source,omitempty"`
	ExitCode *int      `json:"exit_code,omitempty"`
	Signal   string    `json:"signal,omitempty"`
	OOM      bool      `json:"oom_killed,omitempty"`
	Reason   string    `json:"reason,omitempty"`
	// FullScreen is true while the program draws on the alternate screen.
	FullScreen bool `json:"full_screen,omitempty"`
	// Claimed is true when State, Source and Reason come from the guest
	// inside a wall rather than from the trusted side.
	Claimed bool `json:"claimed,omitempty"`
}

// Restart is the respawn policy (v2).
type Restart struct {
	Policy      string `json:"policy"`
	MaxAttempts int    `json:"max_attempts"`
}

// UnmarshalJSON keeps unknown fields.
func (m *Manifest) UnmarshalJSON(b []byte) error {
	type plain Manifest
	if err := json.Unmarshal(b, (*plain)(m)); err != nil {
		return fmt.Errorf("decode the manifest: %w", err)
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		return fmt.Errorf("decode the manifest: %w", err)
	}
	known := map[string]bool{"schema": true, "id": true, "name": true, "created_at": true, "updated_at": true,
		"last_activity_at": true, "last_attached_at": true, "host_version": true, "holder_binary": true,
		"agent": true, "cwd": true, "env": true, "term": true, "limits": true, "runtime": true, "status": true,
		"labels": true, "restart": true, "git": true, "setup_hook": true, "resume_hook": true, "stop_hook": true, "run_hook": true,
		"setup_hash": true, "secrets": true, "missing_secrets": true, "debug_until": true,
		"wall": true, "guest": true, "image": true, "agents": true, "granted_ports": true, "usage": true,
		"host_name": true, "sandbox_name": true, "copy_in": true, "copies_pending": true, "seed_repos": true}
	for k := range all {
		if known[k] {
			delete(all, k)
		}
	}
	m.extra = all
	return nil
}

// MarshalJSON writes unknown fields back.
func (m Manifest) MarshalJSON() ([]byte, error) {
	type plain Manifest
	b, err := json.Marshal(plain(m))
	if err != nil {
		return nil, fmt.Errorf("encode the manifest: %w", err)
	}
	if len(m.extra) == 0 {
		return b, nil
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, fmt.Errorf("encode the manifest: %w", err)
	}
	for k, v := range m.extra {
		all[k] = v
	}
	out, err := json.Marshal(all)
	if err != nil {
		return nil, fmt.Errorf("encode the manifest: %w", err)
	}
	return out, nil
}

// LoadManifest reads one session's manifest.
func LoadManifest(id string) (*Manifest, error) {
	return LoadManifestPath(paths.Manifest(id))
}

// LoadManifestPath reads a session manifest from path.
func LoadManifestPath(path string) (*Manifest, error) {
	clean := filepath.Clean(path)
	root, err := os.OpenRoot(filepath.Dir(clean))
	if err != nil {
		return nil, fmt.Errorf("read the manifest %s: %w", path, err)
	}
	defer root.Close()
	b, err := root.ReadFile(filepath.Base(clean))
	if err != nil {
		return nil, fmt.Errorf("read the manifest %s: %w", path, err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("read the manifest %s: %w", path, err)
	}
	return &m, nil
}

// Save writes the manifest atomically.
func (m *Manifest) Save() error {
	return m.SaveTo(paths.Manifest(m.ID))
}

// HostDir returns the host filesystem path of the session's workspace. For
// walled sessions this is Wall.WorkspaceHost (when set), while Cwd holds the
// guest path inside the wall. For unwalled sessions it returns Cwd.
func (m *Manifest) HostDir() string {
	if m == nil {
		return ""
	}
	if m.Wall != nil && m.Wall.WorkspaceHost != "" {
		return m.Wall.WorkspaceHost
	}
	return m.Cwd
}

// SaveTo writes the manifest atomically to path.
func (m *Manifest) SaveTo(path string) error {
	m.UpdatedAt = time.Now().UTC()
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("save the manifest of session %s: %w", m.ID, err)
	}
	dir := filepath.Dir(path)
	if err := paths.EnsurePrivateDir(dir); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".manifest-*")
	if err != nil {
		return fmt.Errorf("save the manifest of session %s: %w", m.ID, err)
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return fmt.Errorf("save the manifest of session %s: %w", m.ID, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return fmt.Errorf("save the manifest of session %s: %w", m.ID, err)
	}
	tmp.Close()
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("save the manifest of session %s: %w", m.ID, err)
	}
	// Make the rename itself durable, so a shutdown right after cannot lose it.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}

// BootID identifies the current boot, so a respawn can tell that a reboot happened.
func BootID() string {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return string(trimNL(b))
}

func trimNL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}
