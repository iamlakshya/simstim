package client

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/iamlakshya/simstim/internal/brand"
)

// Limits on config files (docs/contributing/cli-spec.md 13.1).
const (
	MaxConfigBytes      = 1 << 20 // 1 MiB per file
	MaxConfigKeyParts   = 64      // parts in one dotted key
	MaxConfigValueDepth = 64      // nested arrays and inline tables
	MaxConfigKeyBytes   = 8 << 20 // total bytes across all full key names
)

// ConfigReadError describes a config file that could not be opened, is not a
// regular file, or exceeds a structural size limit.
type ConfigReadError struct {
	Path       string
	Code       string // "unreadable" or "too_large"
	Msg        string
	Line       int
	Column     int
	NonRegular bool
	Mode       fs.FileMode
	Err        error
}

func (e *ConfigReadError) Error() string {
	if e.Msg != "" {
		return e.Msg
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return "cannot read " + e.Path
}

func (e *ConfigReadError) Unwrap() error { return e.Err }

// SettingsPath returns the path of config.toml.
func SettingsPath() string { return settingsPath() }

// HostsPath returns the path of hosts.toml.
func HostsPath() string { return hostsPath() }

// AbbrevPath replaces the user's home folder prefix with "~/" using forward
// slashes on every platform (docs/contributing/cli-spec.md 13.5).
func AbbrevPath(path string) string {
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		home = filepath.Clean(home)
		clean := filepath.Clean(path)
		if clean == home {
			return "~"
		}
		sep := string(filepath.Separator)
		if rest, ok := strings.CutPrefix(clean, home+sep); ok {
			return "~/" + filepath.ToSlash(rest)
		}
	}
	return filepath.ToSlash(path)
}

// ReadConfigFile opens path once, checks that it is a regular file, and reads
// at most MaxConfigBytes of it. A missing file returns (nil, false, nil).
func ReadConfigFile(path string) (data []byte, exists bool, err error) {
	f, err := openConfigNoBlock(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, &ConfigReadError{
			Path: path,
			Code: "unreadable",
			Msg:  err.Error(),
			Err:  err,
		}
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, true, &ConfigReadError{
			Path: path,
			Code: "unreadable",
			Msg:  err.Error(),
			Err:  err,
		}
	}
	if !fi.Mode().IsRegular() {
		return nil, true, &ConfigReadError{
			Path:       path,
			Code:       "unreadable",
			Msg:        "is not a regular file",
			NonRegular: true,
			Mode:       fi.Mode(),
		}
	}

	b, err := io.ReadAll(io.LimitReader(f, MaxConfigBytes+1))
	if err != nil {
		return nil, true, &ConfigReadError{
			Path: path,
			Code: "unreadable",
			Msg:  err.Error(),
			Err:  err,
		}
	}
	if len(b) > MaxConfigBytes {
		return nil, true, &ConfigReadError{
			Path: path,
			Code: "too_large",
			Msg:  "is larger than 1 MiB",
		}
	}
	return b, true, nil
}

// configCheckHint is the standard Try: line for config errors.
func configCheckHint() string { return brand.Name + " config check" }

// readErrToUser turns a ConfigReadError on hosts.toml into a KindFailed
// UserError pointing to config check (cli-spec 13.4).
func readErrToUser(re *ConfigReadError) error {
	if re.Code == "too_large" {
		return &UserError{
			Msg:  fmt.Sprintf("%s: %s", re.Path, re.Msg),
			Hint: configCheckHint(),
			Code: "config_too_large",
			Kind: KindFailed,
		}
	}
	if re.NonRegular {
		return &UserError{
			Msg:  fmt.Sprintf("%s: %s", re.Path, re.Msg),
			Hint: configCheckHint(),
			Code: "config_unreadable",
			Kind: KindFailed,
		}
	}
	return fmt.Errorf("read %s: %w", re.Path, re.Err)
}
