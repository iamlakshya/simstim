// Package paths names every file and folder the product uses on a host.
package paths

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/brand"
)

func home() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "/"
}

func xdg(env, fallback string) string {
	if v := os.Getenv(env); v != "" && filepath.IsAbs(v) {
		return v
	}
	return filepath.Join(home(), fallback)
}

// StateDir is ~/.local/state/<name>.
func StateDir() string { return filepath.Join(xdg("XDG_STATE_HOME", ".local/state"), brand.Name) }

// DataDir is ~/.local/share/<name>.
func DataDir() string { return filepath.Join(xdg("XDG_DATA_HOME", ".local/share"), brand.Name) }

// ConfigDir is ~/.config/<name>: the user's own settings on a host.
func ConfigDir() string { return filepath.Join(xdg("XDG_CONFIG_HOME", ".config"), brand.Name) }

// AlertsConfig is the alerts file on a host: which events to send, and where.
func AlertsConfig() string { return filepath.Join(ConfigDir(), "alerts.toml") }

// AlertsLog is the log of alert deliveries on a host, one JSON object a line.
// It turns over to the same name with ".1" added.
func AlertsLog() string { return filepath.Join(StateDir(), "alerts.log") }

// AlertsRate counts the alerts every holder on a host sent in the last minute.
// Holders lock it while they read or change it, and while they write the log.
func AlertsRate() string { return filepath.Join(StateDir(), "alerts.rate") }

// SessionsDir holds one folder per session.
func SessionsDir() string { return filepath.Join(StateDir(), "sessions") }

// SessionDir is the folder of one session.
func SessionDir(id string) string { return filepath.Join(SessionsDir(), id) }

// Manifest is the manifest file of one session.
func Manifest(id string) string { return filepath.Join(SessionDir(id), "manifest.json") }

// PasteDir holds files uploaded into a session.
func PasteDir(id string) string { return filepath.Join(SessionDir(id), "paste") }

// HolderLog is the log of one session's holder.
func HolderLog(id string) string { return filepath.Join(SessionDir(id), "holder.log") }

// SupervisorLog is the log of one walled session's supervisor.
func SupervisorLog(id string) string { return filepath.Join(SessionDir(id), "supervisor.log") }

// AuditLog is the append-only JSON-lines audit log of one walled session.
func AuditLog(id string) string { return filepath.Join(SessionDir(id), "audit.jsonl") }

// WorkDir is the workspace folder of one walled session.
func WorkDir(id string) string { return filepath.Join(SessionDir(id), "work") }

// SessionStateDir holds the agent state folders kept across starts of a walled session.
func SessionStateDir(id string) string { return filepath.Join(SessionDir(id), "state") }

// DebugTrace is the debug trace of one session's holder, one JSON object a
// line. It turns over to the same name with ".1" added.
func DebugTrace(id string) string { return filepath.Join(SessionDir(id), "debug.jsonl") }

// SessionDebugDir holds one session's profiles and the saved windows of its
// holder's flight recorder.
func SessionDebugDir(id string) string { return filepath.Join(SessionDir(id), "debug") }

// DebugDir is the debug folder: on a host, the bridges' debug files, and on
// the laptop, the client's.
func DebugDir() string { return filepath.Join(StateDir(), "debug") }

// RuntimeDir holds the sockets. It is $XDG_RUNTIME_DIR/<name> when that exists,
// or /tmp/<name>-<uid>.
func RuntimeDir() string {
	if v := os.Getenv("XDG_RUNTIME_DIR"); v != "" {
		if st, err := os.Stat(v); err == nil && st.IsDir() { //nolint:gosec // G703: the user's own runtime folder, set by their login
			return filepath.Join(v, brand.Name)
		}
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("%s-%d", brand.Name, os.Getuid()))
}

// Socket is the holder socket of one session.
func Socket(id string) string { return filepath.Join(RuntimeDir(), "s", id+".sock") }

// HookSocket is the hook socket of one session. Its holder accepts only hook
// events on it, and it is the only socket the agent's environment names. Its
// path has the same length as Socket's, so that it fits wherever the session
// socket fits: Linux allows 107 bytes, macOS 103.
func HookSocket(id string) string { return filepath.Join(RuntimeDir(), "h", id+".sock") }

// GuestRuntimeDir is the short private folder where an unwalled test driver
// places a guest's local sockets, so they fit inside the Unix socket path limit.
func GuestRuntimeDir(id string) string { return filepath.Join(RuntimeDir(), "g", id) }

// PortDir is the private folder for a walled session's granted port sockets.
func PortDir(id string) string { return filepath.Join(RuntimeDir(), "p", id) }

// PortSocket is the host-side Unix socket for one granted port of a walled
// session: $XDG_RUNTIME_DIR/<name>/p/<id>/<port>.sock (spec 7.4).
func PortSocket(id string, port int) string {
	return filepath.Join(PortDir(id), fmt.Sprintf("%d.sock", port))
}

// SessionDisk is the writable ext4 disk image of one microvm session
// (isolation spec 10.7, 20.3).
func SessionDisk(id string) string { return filepath.Join(SessionDir(id), "disk.img") }

// VsockSocket is the host-side Unix socket that bridges virtio-vsock for one
// microvm session. Kept under RuntimeDir() so it fits the Unix socket path limit.
func VsockSocket(id string) string { return filepath.Join(RuntimeDir(), "v", id+".vsock") }

// VMMSocket is the Cloud Hypervisor API Unix socket for one microvm session.
func VMMSocket(id string) string { return filepath.Join(RuntimeDir(), "v", id+".vmm") }

// BinDir is where versioned helpers are installed.
func BinDir() string { return filepath.Join(DataDir(), "bin") }

// ImagesDir holds converted session images and agents disks on a host.
func ImagesDir() string { return filepath.Join(DataDir(), "images") }

// ArtefactsDir holds host runtime files and temporary probe scratch files.
func ArtefactsDir() string { return filepath.Join(DataDir(), "artefacts") }

// RuntimeArtefactsDir holds pinned microvm runtimes (VMM, ch-remote, guest
// kernel and tools images) on a host.
func RuntimeArtefactsDir() string { return filepath.Join(ArtefactsDir(), "runtime") }

// ArtefactsIncomingDir holds incoming artefacts and the sandbox probe's
// temporary reflink test files (isolation spec 14.3).
func ArtefactsIncomingDir() string { return filepath.Join(ArtefactsDir(), "incoming") }

// WallProbes is the cached wall driver probe results file on a host
// (isolation spec 5.1).
func WallProbes() string { return filepath.Join(StateDir(), "walls.json") }

// EnsurePrivateDir makes dir (and parents) with mode 0700.
func EnsurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("make %s: %w", dir, err)
	}
	//nolint:gosec // G302: a folder needs the execute bit to be opened; 0700 is the owner's alone
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("make %s private: %w", dir, err)
	}
	return nil
}
