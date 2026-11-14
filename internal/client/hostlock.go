package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

// The lock of one host on this computer (docs/contributing/cli-spec.md
// 11.3, rule 13). A command that works on a host's machine takes it first,
// and holds it until it ends: host create, host set, and host rm, across
// its question too. Every save or forget of the host's entry takes it for
// that moment (UpdateHosts). A command that finds it taken fails at once
// with host_busy: none waits for another.

// errNoLocks is the error of a system that has neither flock nor
// LockFileEx. No command claims a lock that it does not hold.
var errNoLocks = errors.New("file locks are not supported on this system")

// lockUnsupported is the lock of a system without file locks: it always
// fails with errNoLocks.
func lockUnsupported() error { return errNoLocks }

// errLockTaken is the error of a lock that another holds.
var errLockTaken = errors.New("the lock is taken")

// hostLockPath is the lock file of host name, beside the folder of its
// record of a removal (removalDir), or "" for a name that cannot name a
// host.
func hostLockPath(name string) string {
	dir := removalDir(name)
	if dir == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(dir), filepath.Base(dir)+".lock")
}

// heldKey marks a context whose command holds the lock of a host. Its
// value is the *heldLock.
type heldKey struct{ name string }

// heldLock is the lock of a host that a command took. Its unlock sets
// gone: a context that outlives the lock no longer says that it holds it.
type heldLock struct {
	gone      atomic.Bool
	forgetMu  sync.Mutex
	forgotten *hostForgetWitness
	stateOnce sync.Once
	state     *hostStateLifetime
	native    *hostOperationFile
	terminal  atomic.Bool
}

// LockHost takes the lock of host name for the command kind, such as
// "host rm", and returns a context that holds it, and the function that
// lets it go. In a context that holds it already, it takes nothing, and
// its function does nothing: a command's steps share its lock. Once the
// lock is let go, the context no longer holds it. It never waits: a lock
// that another command holds is the error host_busy, which names the kind
// of that command. A name that cannot name a host has nothing to lock.
func LockHost(ctx context.Context, name, kind string) (context.Context, func(), error) {
	if err := ctx.Err(); err != nil {
		return ctx, nil, err
	}
	path := hostLockPath(name)
	if path == "" {
		return ctx, func() {}, nil
	}
	if held, ok := ctx.Value(heldKey{name}).(*heldLock); ok {
		if err := held.checkStateOperation(ctx); err != nil {
			return ctx, nil, err
		}
		if held.terminal.Load() {
			return ctx, nil, hostStateRemovalError("local state retirement needs its existing-only retry path")
		}
		if err := held.native.normalHeader(ctx); err != nil {
			return ctx, nil, err
		}
		return ctx, func() {}, nil
	}
	if ctx.Value(hostMembershipContextKey{}) != nil {
		return ctx, nil, hostStateRemovalError("a new OP cannot begin inside a membership callback")
	}
	owned, err := openHostOperationFile(ctx, name)
	if err != nil {
		return ctx, nil, wrapHostOperationError(name, path, err)
	}
	if err := takeLock(owned.file); err != nil {
		defer owned.close()
		if errors.Is(err, errLockTaken) {
			return ctx, nil, busyHostOperation(name, owned)
		}
		return ctx, nil, flockFailed("the host "+name, path, err)
	}
	held := &heldLock{native: owned}
	if err := owned.checkAfterTake(ctx, name, kind, held); err != nil {
		held.gone.Store(true)
		owned.removeOwnedDiagnostic()
		owned.close()
		return ctx, nil, wrapHostOperationError(name, path, err)
	}
	if err := ctx.Err(); err != nil {
		held.gone.Store(true)
		if !held.terminal.Load() {
			owned.removeOwnedDiagnostic()
		}
		owned.close()
		return ctx, nil, err
	}
	unlock := sync.OnceFunc(func() {
		held.gone.Store(true)
		lifetime := held.stateLifetime()
		lifetime.beginClose()
		lifetime.drain()
		clearHostForgetWitness(held)
		if !held.terminal.Load() {
			owned.removeOwnedDiagnostic()
		}
		owned.close()
	})
	return context.WithValue(ctx, heldKey{name}, held), unlock, nil
}

// openLock opens the lock file at path, and makes its folder. what names
// what it locks, for the error.
func openLock(path, what string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, lockFailed(what, path, err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, lockFailed(what, path, err)
	}
	return f, nil
}

// lockFailed is the error of a lock file that could not be made. Its hint
// names the nearest folder on the path that exists, to make writable, or
// the file that stands in the path, to move.
func lockFailed(what, path string, err error) error {
	near := filepath.Dir(path)
	for {
		if _, err := os.Stat(near); err == nil || filepath.Dir(near) == near {
			break
		}
		near = filepath.Dir(near)
	}
	hint := fmt.Sprintf("Make %s writable, then run the command again.", near)
	if st, err := os.Stat(near); err == nil && !st.IsDir() {
		hint = fmt.Sprintf("Move the file %s out of the way, then run the command again.", near)
	}
	return &UserError{Msg: fmt.Sprintf("could not lock %s: %v", what, err), Hint: hint, Code: "lock_failed"}
}

// flockFailed is the error of a lock file that the system could not lock,
// such as on a network disk without file locks (ENOLCK).
func flockFailed(what, path string, err error) error {
	return &UserError{Msg: fmt.Sprintf("could not lock %s: %v", what, err),
		Hint: fmt.Sprintf("Keep %s on a local disk, which has file locks, then run the command again.", filepath.Dir(path)), Code: "lock_failed"}
}

// busy is the error of the lock of host name, which another command holds.
func busy(name, path string) error {
	who := "another command changes it"
	if b, err := os.ReadFile(path + ".who"); err == nil {
		if kind := CleanDetail(strings.TrimSpace(string(b))); kind != "" {
			who = kind + " changes it"
		}
	}
	return &UserError{Msg: fmt.Sprintf("%s is busy: %s", name, who), Hint: "Wait until it ends, then run the command again.", Code: "host_busy"}
}

// SameMachine reports whether a and b name one machine: the same provider,
// ID and created stamp. Lima and Docker give each machine of a host the
// same ID, so the stamp tells them apart. Two hosts without a machine
// count as the same.
func SameMachine(a, b *Machine) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Provider == b.Provider && a.ID == b.ID && a.Created == b.Created
}

func (held *heldLock) checkStateOperation(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if held.gone.Load() || held.native == nil {
		return hostStateRemovalError("the original host operation ended or has no owned descriptor")
	}
	return held.native.check(ctx)
}

func wrapHostOperationError(name, path string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var user *UserError
	if errors.As(err, &user) {
		return err
	}
	return lockFailed("the host "+name, path, err)
}
