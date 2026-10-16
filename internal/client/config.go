// Package client is the laptop side: hosts, SSH, installing the host helper,
// the bridge connection and the attach pump.
package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/iamlakshya/simstim/internal/brand"
)

// Host is one saved host.
type Host struct {
	// Name names the host in commands, as in `attach <name>/<session>`.
	Name string `toml:"name"`
	// SSH says how to reach the host: a Host alias from your SSH config, or
	// user@host[:port].
	SSH string `toml:"ssh"`
	// SSHConfig adds an SSH config file to read as well, for example a repo's
	// own.
	SSHConfig string `toml:"ssh_config,omitempty"`
	// Labels tags the host with key-value pairs of your own, for example
	// provider = "gcp".
	Labels map[string]string `toml:"labels,omitempty"`
	// Provider names a script that starts and stops the host. Optional.
	Provider *Provider `toml:"provider,omitempty"`
	// Machine records the VM, container or sandbox that `host create` made
	// for this host. Hosts you added yourself have none.
	Machine *Machine `toml:"machine,omitempty"`
	// Secrets lists secrets from config.toml that every new session on this
	// host gets, for example ["ANTHROPIC_API_KEY"]. `new --secret` adds more.
	Secrets []string `toml:"secrets,omitempty"`
}

// Settings are the user's preferences, from config.toml. The comments on its
// fields are the config reference: cmd/docgen turns them into
// docs/reference/config.md.
type Settings struct {
	// UI holds the launcher's look and feel.
	UI struct {
		// Motion turns the launcher's animation on or off: "auto" (off inside
		// SSH sessions, Emacs and JetBrains terminals, and with NO_COLOR or
		// TERM=dumb), "on" or "off".
		Motion string `toml:"motion"`
	} `toml:"ui"`
	// Attach holds what happens while you are attached to a session.
	Attach struct {
		// DetachKey leaves the session and keeps it running. Press it twice to
		// send it to the agent.
		DetachKey string `toml:"detach_key"`
		// ImagePasteKey uploads the image on the clipboard to the host and
		// pastes its path. Without an image, the key goes to the agent.
		ImagePasteKey string `toml:"image_paste_key"`
		// Scrollback says what happens to the terminal's scrollback when you
		// attach again: "keep" replays what you missed after what you saw,
		// "clear" starts from a clean screen.
		Scrollback string `toml:"scrollback"`
	} `toml:"attach"`
	// Agents holds how agents start.
	Agents struct {
		// Inline starts each agent that has one in its inline mode, where it
		// draws in the terminal's normal screen, so your terminal keeps its
		// scrollback: Claude Code's classic renderer, Codex with
		// --no-alt-screen. Off by default, so each agent starts as its own
		// defaults say. `new --inline` does it for one session.
		Inline bool `toml:"inline"`
	} `toml:"agents"`
	// Sessions holds what new sessions get.
	Sessions struct {
		// MemoryMax caps the memory of each new session, with everything it
		// starts. "auto" gives the host's default: its memory minus a reserve
		// for the system. A size is a number and a unit in powers of 1024,
		// such as "512M", "4G", "4GiB" or "1.5G", from 256M. A host lowers a
		// size above its limit for all sessions together to that limit.
		// `new --memory-max` sets it for one session.
		MemoryMax string `toml:"memory_max"`
	} `toml:"sessions"`
	// Keymap rebinds launcher commands by name ("session: delete"). A value is
	// a key, a list of keys, or "" to unbind. `config keymap` lists the names.
	Keymap map[string]any `toml:"keymap"`
	// Secrets names environment variables for sessions, each with a command
	// that prints its value on this computer, such as a keychain or password
	// manager read: OPENAI_API_KEY = "op read op://dev/openai/key". A session
	// gets one only when asked (`new --secret`, or a host's secrets). The
	// value goes to that session's environment and is never stored.
	Secrets map[string]string `toml:"secrets"`
}

// DefaultSettings are the settings with nothing in config.toml.
func DefaultSettings() Settings {
	var s Settings
	s.UI.Motion = "auto"
	s.Attach.DetachKey = "ctrl+\\"
	// Windows Terminal keeps Ctrl+V for itself.
	s.Attach.ImagePasteKey = "ctrl+v"
	if runtime.GOOS == "windows" {
		s.Attach.ImagePasteKey = "alt+v"
	}
	s.Attach.Scrollback = "keep"
	s.Sessions.MemoryMax = "auto"
	return s
}

type hostsFile struct {
	Hosts []Host `toml:"host"`
}

// ConfigDir is $XDG_CONFIG_HOME/<name>, or ~/.config/<name> (on macOS too).
func ConfigDir() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" && filepath.IsAbs(v) {
		return filepath.Join(v, brand.Name)
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", brand.Name)
}

// CacheDir is $XDG_CACHE_HOME/<name>, or ~/.cache/<name>.
func CacheDir() string {
	if v := os.Getenv("XDG_CACHE_HOME"); v != "" && filepath.IsAbs(v) {
		return filepath.Join(v, brand.Name)
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".cache", brand.Name)
}

// StateDir is $XDG_STATE_HOME/<name>, or ~/.local/state/<name>.
func StateDir() string {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" && filepath.IsAbs(v) {
		return filepath.Join(v, brand.Name)
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".local", "state", brand.Name)
}

func hostsPath() string    { return filepath.Join(ConfigDir(), "hosts.toml") }
func settingsPath() string { return filepath.Join(ConfigDir(), "config.toml") }

// LoadHosts reads the saved hosts.
func LoadHosts() ([]Host, error) {
	var f hostsFile
	if _, err := toml.DecodeFile(hostsPath(), &f); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", hostsPath(), err)
	}
	return f.Hosts, nil
}

// SaveHosts writes the saved hosts.
func SaveHosts(hosts []Host) error {
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].Name < hosts[j].Name })
	if err := os.MkdirAll(ConfigDir(), 0o700); err != nil {
		return fmt.Errorf("make %s: %w", ConfigDir(), err)
	}
	tmp := hostsPath() + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("save the hosts: %w", err)
	}
	fmt.Fprintf(f, "# Hosts for %s. Edit with `%s host`.\n\n", brand.DisplayName, brand.Name)
	if err := toml.NewEncoder(f).Encode(hostsFile{Hosts: hosts}); err != nil {
		f.Close()
		return fmt.Errorf("save the hosts: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("save the hosts: %w", err)
	}
	if err := os.Rename(tmp, hostsPath()); err != nil {
		return fmt.Errorf("save the hosts: %w", err)
	}
	return nil
}

// SaveHost adds h to the saved hosts, or replaces the host of the same name.
func SaveHost(h Host) error {
	hosts, err := LoadHosts()
	if err != nil {
		return err
	}
	for i := range hosts {
		if hosts[i].Name == h.Name {
			hosts[i] = h
			return SaveHosts(hosts)
		}
	}
	return SaveHosts(append(hosts, h))
}

// FindHost returns the saved host with the given name.
func FindHost(name string) (Host, error) {
	hosts, err := LoadHosts()
	if err != nil {
		return Host{}, err
	}
	for _, h := range hosts {
		if h.Name == name {
			return h, nil
		}
	}
	return Host{}, &UserError{Msg: fmt.Sprintf("no host named %q", name), Hint: fmt.Sprintf("%s host add %s", brand.Name, name)}
}

var hostNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// ValidHostName reports whether n can name a host.
func ValidHostName(n string) bool { return hostNameRE.MatchString(n) }

// LoadSettings reads the settings, with defaults for anything missing.
func LoadSettings() Settings {
	var s Settings
	_, _ = toml.DecodeFile(settingsPath(), &s)
	d := DefaultSettings()
	if s.UI.Motion == "" {
		s.UI.Motion = d.UI.Motion
	}
	if s.Attach.DetachKey == "" {
		s.Attach.DetachKey = d.Attach.DetachKey
	}
	if s.Attach.ImagePasteKey == "" {
		s.Attach.ImagePasteKey = d.Attach.ImagePasteKey
	}
	if s.Attach.Scrollback == "" {
		s.Attach.Scrollback = d.Attach.Scrollback
	}
	if s.Sessions.MemoryMax == "" {
		s.Sessions.MemoryMax = d.Sessions.MemoryMax
	}
	return s
}

// UserError is an error with a suggested next step.
type UserError struct {
	Msg  string
	Hint string
	// Code and Data come from the host, for errors a client acts on, such
	// as a repository's setup script that needs the user's answer.
	Code string
	Data json.RawMessage
}

func (e *UserError) Error() string { return e.Msg }

// SplitTarget splits "host/session" into its parts.
func SplitTarget(t string) (host, session string, err error) {
	host, session, ok := strings.Cut(t, "/")
	if !ok || host == "" || session == "" {
		return "", "", &UserError{Msg: fmt.Sprintf("%q is not host/session", t), Hint: brand.Name + " ls"}
	}
	return host, session, nil
}
