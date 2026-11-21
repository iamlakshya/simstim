package host

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
)

// OwnedSupervisorLease retains the current supervisor's original holder lock.
// It proves native ownership only. An outside startup loan supplies boot intent.
type OwnedSupervisorLease struct {
	managerStop    managerStopSourceV1
	native         *sessionFileLease
	lifetime       context.Context
	generation     string
	cancel         context.CancelFunc
	mu             sync.Mutex
	startup        *originalBootV1
	pending        *pendingStartupV1
	once           sync.Once
	closeErr       error
	prepareCleanup *SandboxPrepareParentCleanupV1
	managerCleanup interface {
		Close() error
		checkNativeRetiredV3(context.Context) error
		CloseDiagnosticsV3() error
	}
	managerDiagnostic error
}

// AcquireOwnedSupervisorLease claims the existing holder lock before any
// manifest, runtime or guest work. A launch descriptor is checked by native
// file identity and private role; its environment number is only a locator.
func AcquireOwnedSupervisorLease(ctx context.Context, id string) (*OwnedSupervisorLease, error) {
	if ctx == nil || !validID(id) {
		return nil, sessionLeaseError("the supervisor has no original owner context")
	}
	var native *sessionFileLease
	var err error
	if os.Getenv(holderLockFDEnv) == "3" {
		_ = os.Unsetenv(holderLockFDEnv)
		unix.CloseOnExec(3)
		file := os.NewFile(3, "inherited-holder-lock")
		if file == nil {
			return nil, sessionLeaseError("the inherited supervisor descriptor is absent")
		}
		native, err = adoptSupervisorFileLease(ctx, id, file)
	} else {
		native, err = openOwnedSessionFileLease(ctx, id, holderLockFile, 500*time.Millisecond)
	}
	if err != nil {
		return nil, err
	}
	var generation [16]byte
	if _, err := rand.Read(generation[:]); err != nil {
		return nil, errors.Join(err, native.Close())
	}
	lifetime, cancel := context.WithCancel(ctx)
	return &OwnedSupervisorLease{native: native, lifetime: lifetime, cancel: cancel, generation: hex.EncodeToString(generation[:])}, nil
}

func adoptSupervisorFileLease(ctx context.Context, id string, file *os.File) (result *sessionFileLease, failure error) {
	root, err := cachefs.OpenPrivateRoot(paths.SessionDir(id), false)
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	defer func() {
		if result == nil {
			failure = errors.Join(failure, file.Close(), root.Close())
		}
	}()
	rootRef, err := cachefs.CaptureRoot(root)
	if err != nil {
		return nil, err
	}
	fileRef, err := cachefs.CaptureFile(root, holderLockFile, file, 0o600)
	if err != nil {
		return nil, err
	}
	// An inherited same-description lock succeeds without releasing it. A
	// different owner still holding this inode refuses the adoption.
	held, err := cachefs.TryLock(file, cachefs.Exclusive)
	if err != nil || !held {
		return nil, errors.Join(sessionLeaseError("the inherited holder lock is not owned by this supervisor"), err)
	}
	lease := &sessionFileLease{root: root, file: file, rootRef: rootRef, fileRef: fileRef,
		id: id, path: paths.SessionDir(id), name: holderLockFile, borrowed: true}
	if err := lease.check(ctx); err != nil {
		return nil, err
	}
	return lease, nil
}

// Valid checks the same native holder ownership and current owner lifetime.
func (owner *OwnedSupervisorLease) Valid(id string) bool {
	return owner != nil && owner.native != nil && owner.native.id == id &&
		owner.lifetime != nil && owner.lifetime.Err() == nil && owner.native.check(owner.lifetime) == nil
}

// Lifetime ends before the original holder descriptor closes.
func (owner *OwnedSupervisorLease) Lifetime() context.Context {
	if owner == nil || owner.lifetime == nil {
		ended, cancel := context.WithCancel(context.Background())
		cancel()
		return ended
	}
	return owner.lifetime
}

// CloseWithError cancels boot work before closing only this native copy.
func (owner *OwnedSupervisorLease) CloseWithError() error {
	if owner == nil {
		return nil
	}
	owner.once.Do(func() {
		if owner.cancel != nil {
			owner.cancel()
		}
		owner.mu.Lock()
		pending, native, managerCleanup := owner.pending, owner.native, owner.managerCleanup
		owner.mu.Unlock()
		if managerCleanup != nil {
			closed := managerCleanup.Close()
			unknown := managerCleanup.checkNativeRetiredV3(context.Background())
			owner.mu.Lock()
			if owner.managerDiagnostic == nil {
				owner.managerDiagnostic = errors.Join(closed, managerCleanup.CloseDiagnosticsV3())
			}
			owner.mu.Unlock()
			owner.closeErr = errors.Join(owner.closeErr, unknown)
		}
		if pending != nil {
			owner.closeErr = errors.Join(owner.closeErr, pending.peer.Close())
		}
		if native != nil {
			owner.closeErr = errors.Join(owner.closeErr, native.Close())
		}
	})
	return owner.closeErr
}

// Close releases this owner after the pending control and runtime have joined.
func (owner *OwnedSupervisorLease) Close() { _ = owner.CloseWithError() }

// Generation is bounded correlation minted by this actual native owner.
// It is data, and cannot issue a lease, receipt or execution grant.
func (owner *OwnedSupervisorLease) Generation() string {
	if owner == nil || owner.native == nil || !owner.Valid(owner.native.id) {
		return ""
	}
	return owner.generation
}

// IsSandboxPrepareBootV1 reports whether this live supervisor accepted the
// closed sandbox-prepare startup loan.
func (owner *OwnedSupervisorLease) IsSandboxPrepareBootV1() bool {
	if owner == nil {
		return false
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	return owner.startup != nil && owner.startup.owner == owner && owner.startup.operation != nil &&
		owner.startup.operation.originalBoot == owner.startup && owner.startup.operation.IsSandboxPrepareBootV1()
}

func (owner *OwnedSupervisorLease) ManagerCleanupDiagnosticsV4() error {
	if owner == nil {
		return nil
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	return owner.managerDiagnostic
}
