package paths

import (
	"os"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/api"
)

// MaxUnixSocketPath is the maximum byte length of a Unix domain socket path
// that fits on both macOS (103 bytes before the trailing NUL) and Linux (107).
const MaxUnixSocketPath = 103

// AgentSocket returns the fixed per-session SSH agent symlink path in
// RuntimeDir(), or "" when id is not a valid session ID.
func AgentSocket(id string) string {
	if !api.ValidSessionID(id) {
		return ""
	}
	return filepath.Join(RuntimeDir(), "agent-"+id+".sock")
}

// EnsurePrivateRuntimeDir ensures RuntimeDir() exists and is safe to hold
// private per-session runtime sockets and locks. Unlike EnsurePrivateDir, it
// rejects symlinks, directories owned by another user, and directories with
// group or other permissions rather than calling chmod on an unsafe path.
func EnsurePrivateRuntimeDir() (string, error) {
	r, err := OpenPrivateRuntimeDir()
	if err != nil {
		return "", err
	}
	defer r.Close()
	return r.Name(), nil
}

// OpenPrivateRuntimeDir returns a capability for the validated runtime
// folder. The caller owns Close. Operations through it keep referring to
// that directory if another process moves or replaces its path.
func OpenPrivateRuntimeDir() (*os.Root, error) { return openPrivateRuntimeDir() }
