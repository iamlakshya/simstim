package chv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/iamlakshya/simstim/internal/paths"
)

const (
	// DefaultSessionDiskBytes is the default session disk cap (10 GiB).
	DefaultSessionDiskBytes int64 = 10 << 30
	// MinSessionDiskBytes is the smallest ext4 session disk we format (16 MiB).
	MinSessionDiskBytes int64 = 16 << 20
)

// FindMkfsExt4 locates mkfs.ext4 on PATH or standard sbin directories (on
// Debian/Ubuntu, /sbin is often omitted from a non-root user's PATH).
func FindMkfsExt4(lookPath func(string) (string, error), stat func(string) (os.FileInfo, error)) (string, error) {
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if stat == nil {
		stat = os.Stat
	}
	if p, err := lookPath("mkfs.ext4"); err == nil && p != "" {
		return p, nil
	}
	for _, candidate := range []string{"/sbin/mkfs.ext4", "/usr/sbin/mkfs.ext4"} {
		if fi, err := stat(candidate); err == nil && !fi.IsDir() {
			return candidate, nil
		}
	}
	return "", errors.New("mkfs.ext4 is not installed on this host. Try: install e2fsprogs (for example, sudo apt-get install -y e2fsprogs)")
}

// EnsureSessionDisk ensures an ext4 session disk exists at path (spec 20.3).
//
// If path already exists as a non-empty regular file, EnsureSessionDisk leaves
// it untouched and NEVER formats it again. Otherwise it creates a sparse file
// of sizeBytes (mode 0600) under a temporary name in the same directory,
// formats it with `mkfs.ext4 -q -F -E root_owner=1000:1000`, and renames it
// atomically into place so a file at path is always complete.
func EnsureSessionDisk(ctx context.Context, path string, sizeBytes int64, mkfsBin string) (created bool, err error) {
	if fi, statErr := os.Stat(path); statErr == nil {
		if !fi.Mode().IsRegular() {
			return false, fmt.Errorf("session disk %s is not a regular file. Try: remove the invalid path and recreate the session", path)
		}
		if fi.Size() > 0 {
			return false, nil
		}
		return false, fmt.Errorf("session disk %s is empty (0 bytes). Try: remove the corrupt disk or recreate the session", path)
	} else if !os.IsNotExist(statErr) {
		return false, fmt.Errorf("stat session disk %s: %w", path, statErr)
	}

	if sizeBytes <= 0 {
		sizeBytes = DefaultSessionDiskBytes
	}
	if sizeBytes < MinSessionDiskBytes {
		return false, fmt.Errorf("session disk size %d bytes is below the 16 MiB minimum. Try: set disk_max to 16M or higher", sizeBytes)
	}
	if mkfsBin == "" {
		mkfsBin, err = FindMkfsExt4(nil, nil)
		if err != nil {
			return false, err
		}
	}

	dir := filepath.Dir(path)
	if err := paths.EnsurePrivateDir(dir); err != nil {
		return false, fmt.Errorf("prepare session disk folder: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".disk-*.img")
	if err != nil {
		return false, fmt.Errorf("create temp session disk: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return false, fmt.Errorf("chmod temp session disk: %w", err)
	}
	if err := tmp.Truncate(sizeBytes); err != nil {
		_ = tmp.Close()
		return false, fmt.Errorf("size temp session disk to %d bytes: %w. Try: check free disk space", sizeBytes, err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("close temp session disk: %w", err)
	}

	// Format with root_owner=1000:1000 so /work and state folders belong to uid/gid 1000 (spec 20.3).
	out, err := exec.CommandContext(ctx, mkfsBin, "-q", "-F", "-E", "root_owner=1000:1000", tmpPath).CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("format session disk with mkfs.ext4 (%s): %w. Try: check e2fsprogs and disk space", strings.TrimSpace(string(out)), err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return false, fmt.Errorf("set session disk permissions: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return false, fmt.Errorf("install session disk: %w", err)
	}
	cleanup = false
	return true, nil
}
