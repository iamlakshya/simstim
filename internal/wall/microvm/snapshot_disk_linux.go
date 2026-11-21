//go:build linux

package microvm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/vmneed"
	"github.com/iamlakshya/simstim/internal/wall"
	"golang.org/x/sys/unix"
)

// snapshotDiskOwnerV1 is created only by the actual prepared runtime factory.
// The disk is captured once after formatting, and this SAME open file becomes
// the jailed VMM's disk. References check native identity, never grant a source.
type snapshotDiskOwnerV1 struct {
	mu                sync.Mutex
	borrowers         sync.WaitGroup
	closing           bool
	closeOnce         sync.Once
	closeErr          error
	session, vm       *os.Root
	sessionRef, vmRef cachefs.Reference
	disk              *os.File
	diskParent        *os.File // same actual VM directory, retained before native disk open
	diskRef           cachefs.Reference
	lease             *os.File // borrowed from the same prepared runtime
	leaseName         string
	bytes             int64
}

func pinSnapshotDiskRootsV1(session *os.Root, lease *os.File, kind backendKind, bytes int64) (*snapshotDiskOwnerV1, error) {
	if session == nil || lease == nil || bytes <= 0 {
		return nil, fmt.Errorf("the original VM disk factory has no retained lease or roots")
	}
	if _, err := cachefs.ValidateFile(session, kind.name()+".lock", lease, 0o600); err != nil {
		return nil, err
	}
	root, err := session.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	owner := &snapshotDiskOwnerV1{session: root, lease: lease, leaseName: kind.name() + ".lock", bytes: bytes}
	// The actual caller must retain this SAME partial owner before handling an
	// error. No deferred Close may lose a materialised root or hide its failure.
	owner.sessionRef, err = cachefs.CaptureRoot(root)
	if err != nil {
		return owner, err
	}
	named, err := root.Lstat("vm")
	if err != nil {
		return owner, err
	}
	if !named.IsDir() || named.Mode()&os.ModeSymlink != 0 {
		return owner, fmt.Errorf("the original VM disk parent is not its actual child directory")
	}
	owner.vm, err = root.OpenRoot("vm")
	if err != nil {
		return owner, err
	}
	// Adopt the actual child Root BEFORE private-mode/reference validation.
	owner.vmRef, err = cachefs.CaptureRoot(owner.vm)
	if err != nil {
		return owner, err
	}
	actual, err := owner.vm.Stat(".")
	if err != nil || !os.SameFile(named, actual) {
		return owner, errors.Join(fmt.Errorf("the actual VM disk parent changed while it was pinned"), err)
	}
	return owner, owner.checkRoots()
}

func (owner *snapshotDiskOwnerV1) checkRoots() error {
	if owner == nil || owner.session == nil || owner.vm == nil || owner.lease == nil {
		return fmt.Errorf("the original VM disk owner has no retained native roots")
	}
	if err := errors.Join(owner.sessionRef.CheckRoot(owner.session), owner.vmRef.CheckRoot(owner.vm)); err != nil {
		return err
	}
	if _, err := cachefs.ValidateFile(owner.session, owner.leaseName, owner.lease, 0o600); err != nil {
		return err
	}
	current, err := owner.session.Lstat("vm")
	if err != nil {
		return err
	}
	info, err := owner.vm.Stat(".")
	if err != nil {
		return err
	}
	if current.Mode()&os.ModeSymlink != 0 || !os.SameFile(current, info) {
		return fmt.Errorf("the original VM disk directory changed native identity")
	}
	return nil
}

func (owner *snapshotDiskOwnerV1) capture(ctx context.Context) error {
	if owner == nil || ctx == nil {
		return fmt.Errorf("the original VM disk has no prepared factory owner")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closing || owner.disk != nil {
		return fmt.Errorf("the original VM disk was already captured or released")
	}
	if err := errors.Join(ctx.Err(), owner.checkRoots()); err != nil {
		return err
	}
	var err error
	if owner.diskParent == nil {
		owner.diskParent, err = owner.vm.Open(".")
		if err != nil {
			return err
		}
	}
	parentInfo, parentErr := owner.diskParent.Stat()
	actualInfo, actualErr := owner.vm.Stat(".")
	if parentErr != nil || actualErr != nil || !os.SameFile(parentInfo, actualInfo) {
		return errors.Join(fmt.Errorf("the retained actual VM disk parent changed"), parentErr, actualErr)
	}
	fd, err := unix.Openat2(int(owner.diskParent.Fd()), SessionDiskName, &unix.OpenHow{Flags: unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return err
	}
	// Attach this SAME native open before any reference/size validation. Abort
	// cleanup belongs to owner.close; no direct failed Close can lose this FD.
	owner.disk = os.NewFile(uintptr(fd), "original-captured-VM-disk")
	owner.diskRef, err = cachefs.CaptureFile(owner.vm, SessionDiskName, owner.disk, 0o600)
	if err != nil || owner.diskRef.Size != owner.bytes {
		return errors.Join(fmt.Errorf("the original VM disk differs from its admitted size or private role"), err)
	}
	return errors.Join(ctx.Err(), owner.checkDisk())
}

func (owner *snapshotDiskOwnerV1) checkDisk() error {
	if err := owner.checkRoots(); err != nil {
		return err
	}
	if owner.disk == nil {
		return fmt.Errorf("the original VM session disk descriptor is absent")
	}
	// Live VMM writes legitimately change mtime. Native inode, single-link
	// ownership, mode, fixed length and anchored leaf identity must still hold.
	if err := owner.diskRef.CheckCreatedFile(owner.vm, SessionDiskName, owner.disk, 0o600); err != nil {
		return err
	}
	info, err := owner.disk.Stat()
	if err != nil || info.Size() != owner.bytes {
		return errors.Join(fmt.Errorf("the original VM session disk changed its fixed size"), err)
	}
	return nil
}

func (owner *snapshotDiskOwnerV1) borrow(ctx context.Context, run func(*os.Root, *os.Root, *os.File) error) error {
	if owner == nil || ctx == nil || run == nil {
		return fmt.Errorf("the snapshot has no original disk owner or borrower")
	}
	owner.mu.Lock()
	if owner.closing {
		owner.mu.Unlock()
		return fmt.Errorf("the original VM disk owner is closing")
	}
	owner.borrowers.Add(1)
	owner.mu.Unlock()
	defer owner.borrowers.Done()
	if err := errors.Join(ctx.Err(), owner.checkDisk()); err != nil {
		return err
	}
	result := run(owner.session, owner.vm, owner.disk)
	return errors.Join(result, ctx.Err(), owner.checkDisk())
}

// close is called only after the actual native child has physically exited,
// or a factory/start failure that created no child. Cancellation is never an
// exit receipt. New borrows are fenced before all original callbacks join.
func (owner *snapshotDiskOwnerV1) close() error {
	if owner == nil {
		return nil
	}
	owner.closeOnce.Do(func() {
		owner.mu.Lock()
		owner.closing = true
		owner.mu.Unlock()
		owner.borrowers.Wait()
		if owner.disk != nil {
			owner.closeErr = errors.Join(owner.closeErr, owner.disk.Close())
		}
		if owner.diskParent != nil {
			owner.closeErr = errors.Join(owner.closeErr, owner.diskParent.Close())
		}
		if owner.vm != nil {
			owner.closeErr = errors.Join(owner.closeErr, owner.vm.Close())
		}
		if owner.session != nil {
			owner.closeErr = errors.Join(owner.closeErr, owner.session.Close())
		}
	})
	return owner.closeErr
}

func (s *sandbox) captureSnapshotDiskV1(ctx context.Context) error {
	s.mu.Lock()
	owner := s.snapshotDisk
	s.mu.Unlock()
	return owner.capture(ctx)
}

func (s *sandbox) closeSnapshotDiskV1() error {
	s.mu.Lock()
	owner := s.snapshotDisk
	s.mu.Unlock()
	return owner.close()
}

func (binding *sandboxNativeBinding) withSnapshotDiskV1(ctx context.Context, connection net.Conn, expected vmneed.NeedV1, borrow func(*os.Root, *os.Root, *os.File) error) error {
	if err := binding.checkNativeBinding(ctx, connection, expected); err != nil {
		return err
	}
	binding.sandbox.mu.Lock()
	owner := binding.sandbox.snapshotDisk
	binding.sandbox.mu.Unlock()
	return owner.borrow(ctx, func(session, vm *os.Root, disk *os.File) error {
		if err := binding.checkNativeBinding(ctx, connection, expected); err != nil {
			return err
		}
		result := borrow(session, vm, disk)
		return errors.Join(result, binding.checkNativeBinding(ctx, connection, expected))
	})
}

func (s *sandbox) Abilities() wall.Abilities {
	s.mu.Lock()
	defer s.mu.Unlock()
	return wall.Abilities{Freeze: s.started && s.helloChecked && !s.physicalExit && s.snapshotDisk != nil, Reflink: s.bootFacts.Reflink}
}
