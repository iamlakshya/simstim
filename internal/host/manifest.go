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
	SetupHook  string `json:"setup_hook,omitempty"`
	ResumeHook string `json:"resume_hook,omitempty"`
	// RunHook is the hook for the next start: the setup hook, or the resume hook after a restart.
	RunHook string `json:"run_hook,omitempty"`
	// Secrets names the variables the client passed at the start. Their
	// values are never written down (secrets.go). MissingSecrets names the
	// ones this run of the agent started without.
	Secrets        []string `json:"secrets,omitempty"`
	MissingSecrets []string `json:"missing_secrets,omitempty"`

	extra map[string]json.RawMessage
}

// AgentInfo is the agent part of a manifest.
type AgentInfo struct {
	Kind           string    `json:"kind"`
	Argv           []string  `json:"argv"`
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
		"labels": true, "restart": true, "git": true, "setup_hook": true, "resume_hook": true, "run_hook": true,
		"secrets": true, "missing_secrets": true}
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
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("save the manifest of session %s: %w", m.ID, err)
	}
	dir := paths.SessionDir(m.ID)
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
	if err := os.Rename(tmp.Name(), filepath.Join(dir, "manifest.json")); err != nil {
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
