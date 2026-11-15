package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
)

// Kernel-held ownership of a session's holder process (holder.lock).
//
// Without systemd, launchHolder locks holder.lock before starting the child
// and passes the open descriptor as fd 3, closing its own copy after fork.
// RunHolder marks fd 3 close-on-exec before starting the shadow terminal or
// the agent, so only the holder process itself holds the lock, and the
// kernel releases it as soon as the holder exits. Under systemd-run,
// RunHolder opens and locks holder.lock as its first step, while unitActive
// covers the moment between systemd-run and RunHolder.

const holderLockFile = "holder.lock"

var holderLockFDEnv = brand.Env("HOLDER_LOCK_FD")

func holderLockPath(id string) string { return filepath.Join(paths.SessionDir(id), holderLockFile) }

// openHolderLockForLaunch opens and locks holder.lock before launchHolder
// spawns a non-systemd holder child. The caller passes the returned *os.File
// in cmd.ExtraFiles (as fd 3) and closes its own copy right after Start.
func openHolderLockForLaunch(id string) (*os.File, error) {
	f, err := os.OpenFile(holderLockPath(id), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open holder lock for session %s: %w", id, err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("a holder already runs for session %s", id)
		}
		return nil, fmt.Errorf("lock holder for session %s: %w", id, err)
	}
	return f, nil
}

// acquireHolderLock claims or adopts holder.lock for the lifetime of
// RunHolder, setting close-on-exec so no child process inherits it.
func acquireHolderLock(id string) (func(), error) {
	owner, err := AcquireOwnedSupervisorLease(context.Background(), id)
	if err != nil {
		return nil, err
	}
	return owner.Close, nil
}

// holderLockHeld reports whether a holder process currently holds
// holder.lock for session id.
func holderLockHeld(id string) bool {
	fd, err := unix.Open(holderLockPath(id), unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return !errors.Is(err, unix.ENOENT) && !errors.Is(err, os.ErrNotExist)
	}
	err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		_ = unix.Flock(fd, unix.LOCK_UN)
		_ = unix.Close(fd)
		return false
	}
	_ = unix.Close(fd)
	return true
}

// holderPendingOrAlive reports whether session id has a live or starting
// holder, even before its socket is open.
func holderPendingOrAlive(id string, m *Manifest) bool {
	return alive(id) || holderLockHeld(id) || (m != nil && m.Run.Unit != "" && unitActive(m.Run.Unit))
}

// unitActive reports whether systemd shows unit as active or starting.
func unitActive(unit string) bool {
	if unit == "" || !SystemdUser() {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := systemctl(ctx, "show", unit, "-p", "ActiveState", "--value")
	if err != nil {
		return true
	}
	switch strings.TrimSpace(string(out)) {
	case "active", "activating", "reloading":
		return true
	default:
		return false
	}
}
