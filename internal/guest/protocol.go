// Package guest runs inside a session's wall as the guest init process
// (<helper> wall-init). It speaks internal/link over fd 3 to the supervisor
// outside the wall, starts the session holder in guest mode, relays holder
// streams byte-for-byte to the guest holder's local socket, accepts pasted
// uploads, sends heartbeats and reaps orphaned children when running as PID 1.
package guest

import (
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// HolderHeader is the JSON header of a link.KindHolder stream.
type HolderHeader struct {
	Session string `json:"session"`
}

// PortHeader is the JSON header of a link.KindPort stream (supervisor -> guest).
type PortHeader struct {
	Port int `json:"port"`
}

// SFTPHeader is the JSON header of a link.KindSFTP stream (supervisor -> guest).
type SFTPHeader struct {
	Session string `json:"session,omitempty"`
}

// ExecHeader is the JSON header of a link.KindExec stream (supervisor -> guest).
type ExecHeader struct {
	ID      string           `json:"id,omitempty"`
	Session string           `json:"session,omitempty"`
	Mode    string           `json:"mode,omitempty"`
	SSH     *api.SSHExecArgs `json:"ssh,omitempty"`
}

// PutHeader is the JSON header of a link.KindPut stream.
type PutHeader struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256,omitempty"`
}

// PutResp is the single JSON reply written by guest init at the end of a
// link.KindPut stream.
type PutResp struct {
	Path   string       `json:"path,omitempty"`
	SHA256 string       `json:"sha256,omitempty"`
	Error  *proto.Error `json:"error,omitempty"`
}

// StartReq is the arguments object of the ctl "start" request.
type StartReq struct {
	Session         string   `json:"session"`
	Name            string   `json:"name,omitempty"`
	Agent           string   `json:"agent"`
	Argv            []string `json:"argv"`
	Env             []string `json:"env"`
	Secrets         []string `json:"secrets,omitempty"`
	Cwd             string   `json:"cwd"`
	Term            string   `json:"term,omitempty"`
	Cols            int      `json:"cols"`
	Rows            int      `json:"rows"`
	RunHook         string   `json:"run_hook,omitempty"`
	Notes           []string `json:"notes,omitempty"`
	AgentID         string   `json:"agent_session_id,omitempty"`
	IDSource        string   `json:"id_source,omitempty"`
	PackageManagers string   `json:"package_managers,omitempty"`
	MinReleaseAge   string   `json:"min_release_age,omitempty"`
	UserEnv         []string `json:"user_env,omitempty"`
}

// StartResp is the result object of the ctl "start" request.
type StartResp struct {
	HolderPID int `json:"holder_pid"`
	AgentPID  int `json:"agent_pid"`
	Cols      int `json:"cols"`
	Rows      int `json:"rows"`
}

// StopReq is the arguments object of the ctl "stop" request.
type StopReq struct {
	GraceMS int `json:"grace_ms"`
}

// Event is one ctl TEvent sent from the guest init to the supervisor.
type Event struct {
	Type       string     `json:"type"`
	Session    string     `json:"session,omitempty"`
	State      string     `json:"state,omitempty"`
	Source     string     `json:"source,omitempty"`
	Reason     string     `json:"reason,omitempty"`
	FullScreen bool       `json:"full_screen,omitempty"`
	Kind       string     `json:"kind,omitempty"`
	Detail     string     `json:"detail,omitempty"`
	TurnNS     int64      `json:"turn_ns,omitempty"`
	AgentID    string     `json:"id,omitempty"`
	IDSource   string     `json:"id_source,omitempty"`
	ExitCode   int        `json:"exit_code,omitempty"`
	Signal     string     `json:"signal,omitempty"`
	Ports      []api.Port `json:"ports,omitempty"`
	Cols       int        `json:"cols,omitempty"`
	Rows       int        `json:"rows,omitempty"`
}
