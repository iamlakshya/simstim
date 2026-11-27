package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
)

// sessionFileLease is the SAME native session/first/holder file-lock engine.
// It retains original checked descriptors; no later name can renew them. A
// descriptor copy for an admitted boot loan is the same OFD, never a reopen.
type sessionFileLease struct {
	mu       sync.RWMutex
	root     *os.Root
	file     *os.File
	rootRef  cachefs.Reference
	fileRef  cachefs.Reference
	id       string
	path     string
	name     string
	borrowed bool
	closed   bool
	once     sync.Once
	closeErr error
}

func sessionLeaseError(message string) error {
	return UserError("session_owner", message, "Keep the session stopped and inspect its original operation and startup owner before retrying.")
}

func openOwnedSessionFileLease(ctx context.Context, id, name string, wait time.Duration) (result *sessionFileLease, failure error) {
	if ctx == nil {
		return nil, sessionLeaseError("the native session lock has no owner context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validID(id) || name != lockFile && name != firstLockFile && name != holderLockFile {
		return nil, sessionLeaseError("the session lock has no exact supported role")
	}
	path := paths.SessionDir(id)
	root, err := cachefs.OpenPrivateRoot(path, false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, UserError("not_found", "no such session", "List sessions to find its ID.")
		}
		return nil, fmt.Errorf("anchor the session lock: %w", err)
	}
	var file *os.File
	defer func() {
		if result == nil {
			if file != nil {
				failure = errors.Join(failure, file.Close())
			}
			failure = errors.Join(failure, root.Close())
		}
	}()
	notFoundIfGone := func(err error) error {
		if ctx.Err() == nil {
			if check, oerr := cachefs.OpenPrivateRoot(path, false); errors.Is(oerr, os.ErrNotExist) {
				return UserError("not_found", "no such session", "List sessions to find its ID.")
			} else if check != nil {
				_ = check.Close()
			}
		}
		return err
	}
	rootRef, err := cachefs.CaptureRoot(root)
	if err != nil {
		return nil, notFoundIfGone(err)
	}
	file, err = cachefs.OpenFile(root, name, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, notFoundIfGone(err)
	}
	deadline := time.Now().Add(wait)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		held, err := cachefs.TryLock(file, cachefs.Exclusive)
		if err != nil {
			return nil, err
		}
		if held {
			break
		}
		if !time.Now().Before(deadline) {
			return nil, UserError("busy", "the session has another operation or startup owner", "Wait until that operation ends, then try again.")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(min(20*time.Millisecond, time.Until(deadline))):
		}
	}
	fileRef, err := cachefs.CaptureFile(root, name, file, 0o600)
	if err != nil {
		return nil, notFoundIfGone(err)
	}
	lease := &sessionFileLease{root: root, file: file, rootRef: rootRef, fileRef: fileRef, id: id, name: name, path: path}
	if err := lease.checkLocked(ctx); err != nil {
		return nil, notFoundIfGone(err)
	}
	return lease, nil
}

func (lease *sessionFileLease) checkLocked(ctx context.Context) error {
	if lease == nil || lease.closed || lease.root == nil || lease.file == nil {
		return sessionLeaseError("the original native session lock ended")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := lease.rootRef.CheckRoot(lease.root); err != nil {
		return sessionLeaseError("the original session directory changed its native identity")
	}
	fresh, err := cachefs.OpenPrivateRoot(lease.path, false)
	if err != nil {
		return sessionLeaseError("the original session directory lost its checked named binding")
	}
	named := lease.rootRef.CheckRoot(fresh)
	closed := fresh.Close()
	if named != nil || closed != nil {
		return errors.Join(sessionLeaseError("the original session directory was replaced"), named, closed)
	}
	if err := lease.fileRef.CheckFile(lease.root, lease.name, lease.file, 0o600); err != nil {
		return sessionLeaseError("the original session lock changed its native identity")
	}
	return ctx.Err()
}

func (lease *sessionFileLease) check(ctx context.Context) error {
	if lease == nil {
		return sessionLeaseError("the session operation has no native lock")
	}
	lease.mu.RLock()
	defer lease.mu.RUnlock()
	return lease.checkLocked(ctx)
}

// duplicateLockedFile is private to the eventual admitted startup sender.
// It establishes no peer/activation permission; the sender must retain its
// actual operation participant and validated outside launch audience.
func (lease *sessionFileLease) duplicateLockedFile(ctx context.Context) (*os.File, error) {
	if lease == nil {
		return nil, sessionLeaseError("the startup loan has no original operation")
	}
	lease.mu.RLock()
	defer lease.mu.RUnlock()
	if lease.borrowed || lease.name != lockFile {
		return nil, sessionLeaseError("only the original owning OP can duplicate a boot loan")
	}
	if err := lease.checkLocked(ctx); err != nil {
		return nil, err
	}
	fd, err := unix.FcntlInt(lease.file.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "startup-op-loan")
	if err := lease.fileRef.CheckFile(lease.root, lease.name, file, 0o600); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

func (lease *sessionFileLease) Close() error {
	if lease == nil {
		return nil
	}
	lease.once.Do(func() {
		lease.mu.Lock()
		defer lease.mu.Unlock()
		lease.closed = true
		// Never issue LOCK_UN on a shared open description. The kernel
		// releases its flock only after the LAST owner/borrow copy closes,
		// including a slow or failed startup whose peer still owns its copy.
		lease.closeErr = errors.Join(lease.closeErr, lease.file.Close(), lease.root.Close())
	})
	return lease.closeErr
}
