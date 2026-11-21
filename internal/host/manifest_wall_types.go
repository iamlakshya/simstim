package host

import (
	"github.com/iamlakshya/simstim/internal/proto"
	"time"
)

// WallRecord is the wall of a walled session, as its supervisor builds it.
type WallRecord struct {
	Level          string    `json:"level"`
	Driver         string    `json:"driver"`
	Version        string    `json:"version,omitempty"`
	Floor          string    `json:"floor,omitempty"`
	DowngradedFrom string    `json:"downgraded_from,omitempty"`
	Reason         string    `json:"reason,omitempty"`
	At             time.Time `json:"at"`
}

// GuestClaims are the last facts that the guest reported about a walled
// session. The supervisor cleans each string before it keeps it. None is
// trusted: the session's row shows the state as claimed (spec 3.4, N8).
type GuestClaims struct {
	Refs       []proto.BundleRef `json:"refs,omitempty"`
	State      string            `json:"state,omitempty"`
	Source     string            `json:"source,omitempty"`
	Reason     string            `json:"reason,omitempty"`
	FullScreen bool              `json:"full_screen,omitempty"`
	At         time.Time         `json:"at,omitzero"`
	AgentPID   int               `json:"agent_pid,omitempty"`
	ExitCode   *int              `json:"exit_code,omitempty"`
	Signal     string            `json:"signal,omitempty"`
}
