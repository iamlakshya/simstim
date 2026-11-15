package host

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/vmconfig"
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
	HostLabel     string                  `json:"host_label,omitempty"`
	SandboxName   string                  `json:"sandbox_name,omitempty"`
	SecretState   *api.SessionSecretState `json:"secret_state,omitempty"`
	Inputs        *api.InputSummary       `json:"inputs,omitempty"`
	CopiesPending bool                    `json:"copies_pending,omitempty"`
	// Agents retains the exact canonical installed-agent disk selected for boot.
	Agents string `json:"agents,omitempty"`
	// VMNeedV1 is opaque protected storage; the sole codec gates every use.
	VMNeedV1 json.RawMessage `json:"-"`
	// RuntimeManifest retains original client runtime data. Only the private
	// operation/startup issuer can project it into execution authority.
	RuntimeManifest *supply.Manifest `json:"runtime_manifest,omitempty"`
	// AutomaticRestore is protected controller intent, never a client option.
	AutomaticRestore json.RawMessage `json:"restore_plan_v1,omitempty"`
	VMCaptureNonce   string          `json:"vm_capture_nonce,omitempty"`
	Wall             *WallRecord     `json:"wall,omitempty"`
	Policy           *api.Policy     `json:"policy,omitempty"`
	Layers           []api.Layer     `json:"layers,omitempty"`
	Armed            bool            `json:"armed,omitempty"`
	Guest            *GuestClaims    `json:"guest,omitempty"`
	Life             *Life           `json:"life,omitempty"`
	Files            *FilePlan       `json:"files,omitempty"`
	SSHAgent         bool            `json:"ssh_agent,omitempty"`
	// SandboxOf is the primary session whose wall and workspace this member uses.
	SandboxOf      string    `json:"sandbox_of,omitempty"`
	Task           string    `json:"task,omitempty"`
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
	SetupHook  string `json:"setup_hook,omitempty"`
	ResumeHook string `json:"resume_hook,omitempty"`
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
	// Only trusted recorded commit identities, never guest claims.
	SeedHeads  map[string]string `json:"seed_heads,omitempty"`
	Pulled     []string          `json:"pulled,omitempty"`
	DebugUntil *time.Time        `json:"debug_until,omitempty"`

	launchStarted bool   // actual owned launch fact, never serialised
	path          string // nonempty only for guest-local manifest files
	extra         map[string]json.RawMessage
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
}

// Runtime is live process information.
type Runtime struct {
	extra         map[string]json.RawMessage
	StartEnvelope bool      `json:"start_envelope,omitempty"`
	Image         string    `json:"image,omitempty"`
	Label         string    `json:"label,omitempty"`
	Artefacts     []string  `json:"artefacts,omitempty"`
	Generation    string    `json:"generation,omitempty"`
	HolderPID     int       `json:"holder_pid,omitempty"`
	AgentPID      int       `json:"agent_pid,omitempty"`
	Unit          string    `json:"unit,omitempty"`
	Socket        string    `json:"socket"`
	BootID        string    `json:"boot_id,omitempty"`
	StartedAt     time.Time `json:"started_at,omitempty"`
	RespawnCount  int       `json:"respawn_count"`
}

// Status is the session's state and why.
type Status struct {
	Claimed  bool      `json:"claimed,omitempty"`
	State    string    `json:"state"`
	Since    time.Time `json:"since"`
	Source   string    `json:"source,omitempty"`
	ExitCode *int      `json:"exit_code,omitempty"`
	Signal   string    `json:"signal,omitempty"`
	OOM      bool      `json:"oom_killed,omitempty"`
	Reason   string    `json:"reason,omitempty"`
	// FullScreen is true while the program draws on the alternate screen.
	FullScreen bool `json:"full_screen,omitempty"`
}

// Restart is the respawn policy (v2).
type Restart struct {
	Policy      string `json:"policy"`
	MaxAttempts int    `json:"max_attempts"`
}

// UnmarshalJSON keeps unknown fields.
func (m *Manifest) UnmarshalJSON(b []byte) error {
	fields, known, err := manifestObject(b)
	if err != nil {
		return fmt.Errorf("decode the manifest: %w", err)
	}
	type plain Manifest
	var decoded plain
	if err := json.Unmarshal(b, &decoded); err != nil {
		return fmt.Errorf("decode the manifest: %w", err)
	}
	*m = Manifest(decoded)
	if raw, ok := fields[vmNeedField]; ok {
		if err := validateStoredVMNeed(m, raw); err != nil {
			return err
		}
	}
	for _, name := range known {
		delete(fields, name)
	}
	m.extra = fields
	if m.VMCaptureNonce != "" && !vmconfig.CaptureNonce(m.VMCaptureNonce) {
		return fmt.Errorf("decode the manifest: invalid accepted VM capture nonce")
	}
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
		return appendStoredVMNeed(&m, b)
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
	return appendStoredVMNeed(&m, out)
}

// LoadManifest reads one session's manifest.
func LoadManifest(id string) (*Manifest, error) {
	b, err := os.ReadFile(paths.Manifest(id))
	if err != nil {
		return nil, fmt.Errorf("read the manifest of session %s: %w", id, err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("read the manifest of session %s: %w", id, err)
	}
	return &m, nil
}

// Save writes the manifest atomically.
func (m *Manifest) Save() error {
	m.UpdatedAt = time.Now().UTC()
	b, err := m.MarshalJSON()
	if err != nil {
		return fmt.Errorf("save the manifest of session %s: %w", m.ID, err)
	}
	file := paths.Manifest(m.ID)
	if m.path != "" {
		file = m.path
	}
	dir := filepath.Dir(file)
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
	if err := closeManifestTemp(tmp); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("close the manifest of session %s: %w", m.ID, err)
	}
	if err := os.Rename(tmp.Name(), file); err != nil {
		// A save that fails again, as each tick tries it, leaves no file.
		os.Remove(tmp.Name())
		return fmt.Errorf("save the manifest of session %s: %w", m.ID, err)
	}
	// Make the rename itself durable, so a shutdown right after cannot lose it.
	if err := commitManifestDirectory(dir); err != nil {
		return fmt.Errorf("save the manifest of session %s: %w", m.ID, err)
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

func LoadManifestFile(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	m.path = path
	return &m, nil
}

func (m *Manifest) SaveTo(path string) error { m.path = path; return m.Save() }
