// Package client is the laptop side: hosts, SSH, installing the host helper,
// the bridge connection and the attach pump.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	tomlv2 "github.com/pelletier/go-toml/v2"

	"github.com/iamlakshya/simstim/internal/api"
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
	// WallFloor is the weakest sandbox level that new sessions on this host
	// may get: "none", "process", "gvisor" or "microvm".
	WallFloor string `toml:"wall_floor,omitempty" json:"-"`
	// Trusted marks a host where you control root. Untrusted hosts default to
	// false and do not receive secrets unless explicitly allowed.
	Trusted bool `toml:"trusted,omitempty" json:"-"`
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
	// Sandbox holds defaults for walled sessions.
	Sandbox struct {
		// Paste sets which file types image paste and drag-and-drop may upload
		// into a walled session: "images-and-pdf" (PNG, JPEG, GIF, WebP and
		// PDF only, the default) or "any". The paste guard still refuses
		// private files and clipboard-written paths in either mode.
		Paste string `toml:"paste"`
		// PackageManagers controls the settings that guest init writes for
		// package managers inside a sandbox: "guarded" (minimum release age,
		// no dependency install scripts for npm, pnpm, Yarn and Bun, and Go
		// proxy settings), "strict" (also blocks pip and uv source builds), or
		// "off".
		PackageManagers string `toml:"package_managers"`
		// Env sets extra environment variables inside a sandbox. A name set
		// here replaces the guest init's value for that name.
		Env map[string]string `toml:"env"`
	} `toml:"sandbox"`
	// Network holds defaults for sandbox network policy.
	Network struct {
		// MinReleaseAge sets the minimum release age for package managers
		// inside a sandbox, as a whole number of days from "0d" to "365d".
		// "0d" turns the age check off.
		MinReleaseAge string `toml:"min_release_age"`
	} `toml:"network"`
	// Keymap rebinds launcher commands by name ("session: delete"). A value is
	// a key, a list of keys, or "" to unbind. `config keymap` lists the names.
	Keymap map[string]any `toml:"keymap"`
	// Secrets names secrets for sessions, each as a command string or a
	// [secrets.NAME] table with run (the command that prints the value) and
	// optional hosts, header, format and route bindings. In a walled session,
	// secrets whose hosts come from [secrets.NAME] or the agent table stay in
	// the egress process and never enter the sandbox. Values are never stored.
	Secrets map[string]any `toml:"secrets"`
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
	s.Sandbox.Paste = "images-and-pdf"
	s.Sandbox.PackageManagers = api.DefaultPackageManagers
	s.Network.MinReleaseAge = api.DefaultMinReleaseAge
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

// LoadHosts reads the saved hosts. A file that cannot be read, is not a
// regular file, or is larger than 1 MiB fails with KindFailed. A file that
// does not decode or defines two case-folded spellings of one field fails
// with KindUsage and points to `config check` (cli-spec 13.4).
func LoadHosts() ([]Host, error) {
	b, exists, err := ReadConfigFile(hostsPath())
	if err != nil {
		var re *ConfigReadError
		if errors.As(err, &re) {
			return nil, readErrToUser(re)
		}
		return nil, fmt.Errorf("read %s: %w", hostsPath(), err)
	}
	if !exists {
		return nil, nil
	}
	var f hostsFile
	ins, err := DecodeConfigLoose(hostsPath(), b, &f)
	if err != nil {
		var re *ConfigReadError
		if errors.As(err, &re) {
			return nil, readErrToUser(re)
		}
		return nil, tomlError(hostsPath(), err)
	}
	if len(ins.Aliases) > 0 {
		return nil, aliasError(hostsPath(), ins.Aliases[0])
	}
	return f.Hosts, nil
}

// tomlError turns a go-toml/v2 decode error into a KindUsage UserError with
// file, line, column and a Try: hint pointing to config check.
func tomlError(path string, err error) error {
	msg, line, col := err.Error(), 0, 0
	var de *tomlv2.DecodeError
	if errors.As(err, &de) {
		line, col = de.Position()
		msg = strings.TrimPrefix(de.Error(), "toml: ")
	}
	where := path
	if line > 0 {
		where = fmt.Sprintf("%s, line %d, column %d", path, line, col)
	}
	return &UserError{
		Msg:  where + ": " + strings.TrimPrefix(msg, "toml: "),
		Hint: configCheckHint(),
		Code: "bad_config",
		Kind: KindUsage,
	}
}

func aliasError(path string, a FieldAlias) error {
	where := path
	if a.Pos.Line > 0 {
		where = fmt.Sprintf("%s, line %d, column %d", path, a.Pos.Line, a.Pos.Column)
	}
	scope := "top level"
	if a.Table != "" {
		scope = "[" + a.Table + "]"
	}
	return &UserError{
		Msg:  fmt.Sprintf("%s: %q and %q both name %q in %s", where, a.First, a.Second, a.Canonical, scope),
		Hint: configCheckHint(),
		Code: "bad_config",
		Kind: KindUsage,
	}
}

// SaveHosts writes the saved hosts. It is the one function that writes
// hosts.toml, and it writes nothing once ctx has ended: a cancel that came
// before the save keeps the file as it was (StoppedError).
func SaveHosts(ctx context.Context, hosts []Host) error {
	if err := ctx.Err(); err != nil {
		return stoppedBefore("save the hosts", err)
	}
	unlock, err := lockHostsFile(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	return lockedSaveHosts(ctx, hosts)
}

func lockedSaveHosts(ctx context.Context, hosts []Host) error {
	if err := ctx.Err(); err != nil {
		return stoppedBefore("save the hosts", err)
	}
	cp := make([]Host, len(hosts))
	copy(cp, hosts)
	sort.Slice(cp, func(i, j int) bool { return cp[i].Name < cp[j].Name })
	if err := os.MkdirAll(ConfigDir(), 0o700); err != nil {
		return fmt.Errorf("make %s: %w", ConfigDir(), err)
	}
	f, err := os.CreateTemp(ConfigDir(), "hosts.toml.tmp-*")
	if err != nil {
		return fmt.Errorf("save the hosts: %w", err)
	}
	tmp := f.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("save the hosts: %w", err)
	}
	fmt.Fprintf(f, "# Hosts for %s. Edit with `%s host`.\n\n", brand.DisplayName, brand.Name)
	if err := tomlv2.NewEncoder(f).Encode(hostsFile{Hosts: cp}); err != nil {
		_ = f.Close()
		return fmt.Errorf("save the hosts: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("save the hosts: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return stoppedBefore("save the hosts", err)
	}
	if err := os.Rename(tmp, hostsPath()); err != nil {
		return fmt.Errorf("save the hosts: %w", err)
	}
	cleanup = false
	return nil
}

// SaveHost adds h to the saved hosts, or replaces the host of the same
// name, through lockedSaveHosts: nothing once ctx has ended.
func SaveHost(ctx context.Context, h Host) error {
	if err := ctx.Err(); err != nil {
		return stoppedBefore("save the hosts", err)
	}
	unlock, err := lockHostsFile(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	hosts, err := LoadHosts()
	if err != nil {
		return err
	}
	for i := range hosts {
		if hosts[i].Name == h.Name {
			hosts[i] = h
			return lockedSaveHosts(ctx, hosts)
		}
	}
	return lockedSaveHosts(ctx, append(hosts, h))
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

var hostNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// ValidHostName reports whether n can name a host.
func ValidHostName(n string) bool { return hostNameRE.MatchString(n) }

// LoadSettings reads the settings, with defaults for anything missing. If
// config.toml is unreadable, broken or defines case-folded aliases of one
// field, it returns the defaults. Callers that want the warning use
// LoadSettingsChecked.
func LoadSettings() Settings {
	s, _ := LoadSettingsChecked()
	return s
}

// LoadSettingsChecked reads config.toml once as a bounded regular file and
// returns the effective settings plus a warning when the file could not be
// used (docs/contributing/cli-spec.md 13.4).
func LoadSettingsChecked() (Settings, string) {
	d := DefaultSettings()
	b, exists, err := ReadConfigFile(settingsPath())
	if err != nil {
		var re *ConfigReadError
		if errors.As(err, &re) && re.Code == "too_large" {
			return d, fmt.Sprintf("config.toml %s, so its settings are not used", re.Msg)
		}
		return d, "config.toml cannot be read, so its settings are not used"
	}
	if !exists {
		return d, ""
	}
	var s Settings
	ins, err := DecodeConfigLoose(settingsPath(), b, &s)
	if err != nil {
		var re *ConfigReadError
		if errors.As(err, &re) && re.Code == "too_large" {
			return d, fmt.Sprintf("config.toml %s, so its settings are not used", re.Msg)
		}
		var de *tomlv2.DecodeError
		if errors.As(err, &de) {
			if line, _ := de.Position(); line > 0 {
				return d, fmt.Sprintf("config.toml has an error on line %d, so its settings are not used", line)
			}
		}
		return d, "config.toml has an error, so its settings are not used"
	}
	if len(ins.Aliases) > 0 {
		a := ins.Aliases[0]
		scope := "at the top level"
		if a.Table != "" {
			scope = "in [" + a.Table + "]"
		}
		return d, fmt.Sprintf("config.toml names %q and %q for the same setting %s, so its settings are not used", a.First, a.Second, scope)
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
	if s.Sandbox.Paste == "" {
		s.Sandbox.Paste = d.Sandbox.Paste
	}
	if s.Sandbox.PackageManagers == "" {
		s.Sandbox.PackageManagers = d.Sandbox.PackageManagers
	}
	if s.Network.MinReleaseAge == "" {
		s.Network.MinReleaseAge = d.Network.MinReleaseAge
	}
	return s, ""
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
