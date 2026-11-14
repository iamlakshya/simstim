package host

import "github.com/iamlakshya/simstim/internal/brand"

// The guest's own folders, as the agent sees them (spec 4.2 and 7.1).
const (
	// GuestHome is the home of the user agent, fresh at each start.
	GuestHome = "/home/agent"
	// GuestUser is the user that the holder and the agent run as.
	GuestUser = "agent"
)

// GuestRunDir holds the holder's socket, its hook socket and its manifest,
// inside the sandbox.
func GuestRunDir() string { return "/run/" + brand.Name }

// GuestHookSocket is the hook socket of a session inside its sandbox: the
// only socket that the agent's environment names.
func GuestHookSocket() string { return GuestRunDir() + "/hook.sock" }
