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

// BinDir is where versioned helpers are installed.
func BinDir() string { return filepath.Join(DataDir(), "bin") }

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
