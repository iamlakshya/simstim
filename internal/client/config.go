// Package client is the laptop side: hosts, SSH, installing the host helper,
// the bridge connection and the attach pump.
package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

// Host is one saved host.
type Host struct {
	// Private once-only creation capability; never serialized or metadata authority.
	machineSourceBirth *hostMachineSourceBirth `toml:"-" json:"-"`
	// WallFloor is the host's actual pinned minimum wall level.
	WallFloor string `toml:"wall_floor,omitempty"`
	// Place controls this host's automatic-placement pool and local features.
	Place HostPlacement `toml:"place,omitempty"`
	// Prepared records the last observed preparation facts; it grants no start.
	Prepared *PreparedHost `toml:"prepared,omitempty"`
	api.LayerValues
	// Lock fixes values and denies for every session on this host.
	Lock *api.LayerValues `toml:"lock,omitempty"`

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
	// Repos lists repositories that may be sent to this host in clone mode.
	Repos []string `toml:"repos,omitempty"`
	pool  *bool
}

// WithPool returns a copy with an explicit automatic-placement pool setting.
func (h Host) WithPool(inPool bool) Host { v := inPool; h.Place.Pool, h.pool = &v, nil; return h }

// Pool reports the explicit automatic-placement setting, if any.
func (h Host) Pool() (inPool, set bool) {
	if h.Place.Pool != nil {
		return *h.Place.Pool, true
	}
	if h.pool == nil {
		return false, false
	}
	return *h.pool, true
}

// Settings are the user's preferences, from config.toml. The comments on its
// fields are the config reference: cmd/docgen turns them into
// docs/reference/config.md.
type Settings struct {
	// Placement selects the order and wake policy of new auto.
	Placement PlacementSettings `toml:"placement"`
	// Images selects how runtime files and images reach a host.
	Images struct {
		// Fetch is auto, host or laptop. Auto uses the host when it can reach
		// the release, then falls back to the laptop once if that fetch fails.
		// Host downloads on the host. Laptop copies checked files from here.
		Fetch string `toml:"fetch"`
	} `toml:"images"`

	api.LayerValues
	// Lock fixes values and denies that narrower layers may not change.
	Lock *api.LayerValues `toml:"lock,omitempty"`
	// Profiles names policy layers selected after the repository.
	Profiles map[string]api.LayerValues `toml:"profiles,omitempty"`
	// Repos names policy layers by origin, without a scheme or .git.
	Repos map[string]RepoSettings `toml:"repos,omitempty"`

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
	Secrets map[string]SecretDefinition `toml:"secrets"`
	// SecretTiers sets the lowest tier that may receive each named secret.
	// Unlisted names require own; on_hosts is an additional saved-host gate.
	SecretTiers map[string]string `toml:"secret_tiers,omitempty"`
	loadErr     error
}

// DefaultSettings are the settings with nothing in config.toml.
func DefaultSettings() Settings {
	var s Settings
	s.Placement = PlacementSettings{Strategy: "spread", Wake: "ask"}
	s.Images.Fetch = "auto"
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
	// Default is a saved host name, auto, or empty when none is selected.
	Default string `toml:"default,omitempty"`
	Hosts   []Host `toml:"host"`
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

// ConfigFile is one of the client's config files.
type ConfigFile struct {
	// Name is the file's name, such as "config.toml".
	Name string
	// Path is where the file lives.
	Path string
	// Type is the struct that the file decodes into. Its TOML tags are the
	// file's known keys.
	Type reflect.Type
}

// ConfigFiles lists the config files: config.toml, then hosts.toml.
// `config path` prints their paths, and `config check` reads them.
func ConfigFiles() []ConfigFile {
	return []ConfigFile{
		{Name: "config.toml", Path: settingsPath(), Type: reflect.TypeFor[Settings]()},
		{Name: "hosts.toml", Path: hostsPath(), Type: reflect.TypeFor[hostsFile]()},
	}
}

// LoadHosts reads the saved hosts, through ReadConfigFile. A file that
// cannot be read is an error of the file system. One that is not a regular
// file, or is too large, cannot be used, and says so. A file that does not
// decode is the user's to fix: a usage error that says where.
func LoadHosts() ([]Host, error) {
	file, err := loadHostsFile()
	return file.Hosts, err
}

// tomlLine finds the line in the error of a value of the wrong type, which
// the TOML package gives only in its text.
var tomlLine = regexp.MustCompile(`^toml: line (\d+)`)

// TOMLWhere reads the error of a TOML file that does not decode: what went
// wrong, and its line and column, or 0 when the error gives none. data is
// the file's text.
func TOMLWhere(data string, err error) (msg string, line, col int) {
	msg = err.Error()
	var pe toml.ParseError
	if errors.As(err, &pe) {
		msg, line, col = pe.Message, pe.Position.Line, pe.Position.Col
	} else if m := tomlLine.FindStringSubmatch(msg); m != nil {
		// A type error names its line and key: point at the line's first word.
		line, _ = strconv.Atoi(m[1])
		if _, rest, ok := strings.Cut(msg, "): "); ok {
			msg = rest
		}
		if lines := strings.Split(data, "\n"); line >= 1 && line <= len(lines) {
			col = len(lines[line-1]) - len(strings.TrimLeft(lines[line-1], " \t")) + 1
		}
	}
	return strings.TrimPrefix(msg, "toml: "), line, col
}

// tomlError is the usage error of a TOML file that does not decode, with
// the line and column of the mistake (docs/contributing/cli-spec.md 13.4).
func tomlError(path, data string, err error) error {
	msg, line, col := TOMLWhere(data, err)
	where := path
	if line > 0 {
		where = fmt.Sprintf("%s, line %d, column %d", path, line, col)
	}
	return &UserError{Msg: where + ": " + msg, Hint: brand.Name + " config check", Code: "bad_config", Kind: KindUsage}
}

// SaveHosts replaces the saved host list under its file lock. A caller
// changing one entry uses SaveHost or ForgetHostContext instead, so it
// cannot overwrite another command's change to a different host.
func SaveHosts(ctx context.Context, hosts []Host) error { return SaveHostsContext(ctx, hosts) }

// SaveHostsContext replaces the whole list through H3's one locked writer,
// while preserving cancellation for existing context-bearing callers.
func SaveHostsContext(ctx context.Context, hosts []Host) error {
	if err := ctx.Err(); err != nil {
		return stoppedBefore("save the hosts", err)
	}
	return saveHostsWithOPAdmission(ctx, hosts)
}

// SaveHost adds h to the saved hosts, or replaces the host of the same name,
// in one locked read, change and write of hosts.toml.
func SaveHost(h Host) error { return SaveHostContext(context.Background(), h) }

// SaveHostContext changes one saved entry without losing another writer.
func SaveHostContext(ctx context.Context, h Host) error {
	if err := ctx.Err(); err != nil {
		return stoppedBefore("save the hosts", err)
	}
	was, err := SavedEntry(h.Name)
	if err != nil {
		return err
	}
	return UpdateHosts(ctx, HostChange{Kind: "host save", Was: was, Now: &h})
}

// StoppedError is the error of a change that a cancel or a timeout came
// before: nothing was changed. It is a UserError, of KindCancelled after a
// cancel, and it is the context's error too, for errors.Is.
type StoppedError struct {
	*UserError
	Err error
}

// Unwrap gives the UserError and the context's error.
func (e *StoppedError) Unwrap() []error { return []error{e.UserError, e.Err} }

// stoppedBefore is the StoppedError of what, which err stopped.
func stoppedBefore(what string, err error) error {
	ue := &UserError{Msg: "cancelled before it could " + what + "; nothing was changed", Hint: "Run the command again.", Code: "cancelled", Kind: KindCancelled}
	if errors.Is(err, context.DeadlineExceeded) {
		ue = &UserError{Msg: "timed out before it could " + what + "; nothing was changed", Hint: "Run the command again.", Code: "timeout"}
	}
	return &StoppedError{UserError: ue, Err: err}
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
	names := make([]string, len(hosts))
	for i, h := range hosts {
		names[i] = h.Name
	}
	// A name that can name a host is safe to repeat in a command. Another
	// one, with a space, a ; or a leading dash, is not.
	hint := fmt.Sprintf("%s host add %s", brand.Name, name)
	if !ValidHostName(name) {
		hint = fmt.Sprintf("Name one of the hosts that %s host ls lists.", brand.Name)
	}
	return Host{}, &UnknownNameError{
		UserError: &UserError{Msg: fmt.Sprintf("no host named %q", name), Hint: hint, Code: "host_not_found", Kind: KindNotFound},
		Name:      name, Known: names,
	}
}

// ValidHostName reports whether n can name a saved host.
func ValidHostName(n string) bool {
	var state hostNameValidation
	for _, r := range n {
		state.Rune(r)
	}
	return state.Valid()
}

// HostFileKey names the files of the host called name: its name in lower
// case, cut to 32 bytes at most and with no dot or dash at its end, a dash,
// and 32 hex characters (128 bits) of SHA-256 over the exact name. The hash
// tells apart two names that differ only in case, which a disk that ignores
// case would take for one file; the name only helps a person read it. Every
// file of a host's own uses it: its pin, the pin's lock and the pin's
// temporary files now, and later its SSH config. It is "" for a name that
// cannot name a host, from which no path is ever built.
func HostFileKey(name string) string {
	if !ValidHostName(name) {
		return ""
	}
	sum := sha256.Sum256([]byte(name))
	p := strings.ToLower(name)
	if len(p) > 32 {
		p = p[:32]
	}
	return strings.TrimRight(p, ".-") + "-" + hex.EncodeToString(sum[:16])
}

// SettingsWarning is the warning of a config.toml that does not decode or
// cannot be read, or "" when it decodes or does not exist. LoadSettings
// then gives the defaults (docs/contributing/cli-spec.md 13.4).
func SettingsWarning() string {
	_, w := ReadSettings()
	return w
}

// LoadSettings reads the settings, with defaults for anything missing. A
// config.toml that does not decode, or cannot be read, gives the defaults
// alone, and the CLI and the launcher warn of it (SettingsWarning).
func LoadSettings() Settings {
	s, _ := ReadSettings()
	return s
}

// ReadSettings reads config.toml once, through ReadConfigFile, for its
// settings and its warning: the warning of a file that does not decode,
// cannot be read or is too large, or "". With a warning, the settings are
// the defaults alone.
func ReadSettings() (Settings, string) {
	operation, err := openSettingsOperation(context.Background())
	if err != nil {
		return failedSettingsRead(err)
	}
	settings := operation.settings
	if err := operation.Close(); err != nil {
		return failedSettingsRead(settingsOperationFailure(err))
	}
	return settings, ""
}

// withDefaults fills each setting that s leaves empty with its default.
func withDefaults(s Settings) Settings {
	d := DefaultSettings()
	if s.Placement.Strategy == "" {
		s.Placement.Strategy = d.Placement.Strategy
	}
	if s.Placement.Wake == "" {
		s.Placement.Wake = d.Placement.Wake
	}
	if s.Images.Fetch == "" {
		s.Images.Fetch = d.Images.Fetch
	}
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

// UserError is an error with a suggested next step: the message, and the
// "Try:" line (docs/contributing/cli-spec.md, section 4).
type UserError struct {
	Msg  string
	Hint string
	// Code names the error, such as host_not_found. A host sends its own
	// codes, and the client acts on some of them, such as a repository's
	// setup script that needs the user's answer. Data carries what the
	// client needs for that.
	Code string
	Data json.RawMessage
	// Kind sets the exit code. The zero Kind is KindFailed.
	Kind Kind
}

func (e *UserError) Error() string { return e.Msg }

// SplitTarget splits "host/session" into its parts.
func SplitTarget(t string) (host, session string, err error) {
	host, session, ok := strings.Cut(t, "/")
	if !ok || host == "" || session == "" {
		return "", "", &UserError{Msg: fmt.Sprintf("%q is not host/session", t), Hint: brand.Name + " ls", Code: "bad_address", Kind: KindUsage}
	}
	return host, session, nil
}
