package proto

import "github.com/iamlakshya/simstim/internal/secretmode"

// The ctl stream of a link carries the ops of a walled session (spec
// 10.3). The supervisor sends each op as a TRequest, and the guest init
// answers with a TResponse that carries the same ID. The guest init sends
// events as TEvent frames. Both ends run one version of the helper, so
// these types change together.

// The ops of the ctl stream, from the supervisor to the guest init.
const (
	// CtlStart starts the session's holder, which starts the agent.
	CtlStart = "start"
	// CtlStop stops the holder and its agent, then the guest init exits.
	CtlStop = "stop"
)

// The events of the ctl stream, from the guest init to the supervisor.
// Each is a claim: the supervisor cleans it, keeps it apart from its own
// facts and shows it as claimed.
const (
	EventStatus    = "status"    // the session's state, as the guest sees it
	EventAlert     = "alert"     // an alert that the guest raises
	EventAgentID   = "agent_id"  // the agent's own session ID, from a hook
	EventEnded     = "ended"     // the agent ended
	EventHeartbeat = "heartbeat" // the guest init is alive, every 5 s
)

// CtlStartArgs are the arguments of the op start. The environment is the
// whole of the agent's environment: the trusted side made it from a fixed
// base, never from the host's login environment (spec 4.2).
type CtlStartArgs struct {
	// PackageManagers is one immutable cold-start guest settings plan.
	PackageManagers *PackageManagersV1 `json:"package_managers,omitempty"`
	// Decoys is private ephemeral cold-start data, never a saved row or label.
	Decoys *DecoyFilesV1 `json:"decoys,omitempty"`
	// PreparedClaim consumes one run-owned member preparation, once.
	PreparedClaim string `json:"prepared_claim,omitempty"`
	// SecretModes is nil for legacy startup and explicit even when empty.
	SecretModes map[string]secretmode.SecretMode `json:"secret_modes,omitempty"`
	// InputNonce and InputDigest require completion of the reserved input phase.
	InputNonce  string `json:"input_nonce,omitempty"`
	InputDigest string `json:"input_digest,omitempty"`
	DNS         string `json:"dns,omitempty"`
	// RunToken correlates this member only with its controller-owned run.
	RunToken string `json:"run_token,omitempty"`
	// DefaultShell selects the guest image login shell when Shell is empty.
	DefaultShell bool   `json:"default_shell,omitempty"`
	Shell        string `json:"shell,omitempty"`
	// Proxy and CACert carry only the established public egress trust contract.
	Proxy  string `json:"proxy,omitempty"`
	CACert string `json:"ca_cert,omitempty"`
	// PendingMembers keeps init alive while saved sibling commands restart.
	PendingMembers int `json:"pending_members,omitempty"`
	// StateFiles is the validated home-relative file-family plan.
	StateFiles []string `json:"state_files,omitempty"`
	// CaptureNonce identifies only this run's fresh incoming generation.
	CaptureNonce string   `json:"capture_nonce,omitempty"`
	Session      string   `json:"session"`
	Agent        string   `json:"agent,omitempty"` // the agent's kind, for its status rules
	Argv         []string `json:"argv"`
	Env          []string `json:"env"`
	Cwd          string   `json:"cwd"`
	Cols         int      `json:"cols"`
	Rows         int      `json:"rows"`
	Hooks        []string `json:"hooks,omitempty"`
	// Secrets are the values of the exposed secrets. The guest init hands
	// them to the holder over its socket, as the host does for a session
	// without a wall, so that no file holds them.
	Secrets map[string]string `json:"secrets,omitempty"`
	// Missing names the secrets that the trusted side does not have, such
	// as after a host restart. The holder says in the agent's terminal that
	// the agent starts without them.
	Missing []string `json:"missing,omitempty"`
	// Restored is true when restore started the session again, after a
	// host restart.
	Restored bool `json:"restored,omitempty"`
}

// CtlStartResult answers start.
type CtlStartResult struct {
	GuestABI string `json:"guest_abi"`
	AgentPID int    `json:"agent_pid"`
}

// CtlStopArgs are the arguments of the op stop.
type CtlStopArgs struct {
	GraceMS int `json:"grace_ms"`
}

// CtlEvent is one event from the guest init. Event names the kind, and
// only the fields of that kind are set.
type CtlEvent struct {
	// GuestMemoryV1 supplies flat current/max/events only on a mem claim.
	*GuestMemoryV1
	RunToken string `json:"run_token,omitempty"`
	// CaptureNonce is present only on state_ready, never a host path.
	CaptureNonce string `json:"capture_nonce,omitempty"`
	Event        string `json:"event"`
	// Session is the session that a status, alert, agent_id or ended event
	// is about.
	Session string `json:"session,omitempty"`
	// status
	State      string `json:"state,omitempty"`
	Source     string `json:"source,omitempty"`
	Reason     string `json:"reason,omitempty"`
	FullScreen bool   `json:"full_screen,omitempty"`
	// alert
	Kind   string `json:"kind,omitempty"`
	Detail string `json:"detail,omitempty"`
	// agent_id
	ID string `json:"id,omitempty"`
	// ended
	ExitCode *int   `json:"exit_code,omitempty"`
	Signal   string `json:"signal,omitempty"`
}
