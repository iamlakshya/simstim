//go:build linux

package host

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"golang.org/x/sys/unix"
)

// This factory derives code from its own actual mapped original descriptor.
// It creates no OP, session subject, attempt transaction or cleanup permission
// for another resource. There is no path/hash/file-info adoption constructor.
type purgeAttemptCodeV2 struct {
	mu                      sync.Mutex
	origin                  *placement
	source                  *os.File
	sourceInfo              os.FileInfo
	home                    *os.Root
	homeRef                 purgeRef
	directory               *os.Root
	directoryRef            purgeRef
	directoryLeaf           string
	directoryCapture        string
	copy                    *os.File
	copyCreated             cachefs.Reference
	copyInfo                os.FileInfo
	copyReference           cachefs.Reference
	copyLeaf                string
	lease                   *os.File
	leaseCreated            cachefs.Reference
	leaseInfo               os.FileInfo
	leaseLeaf               string
	loans                   map[*purgeAttemptCodeLoanV2]struct{}
	execution               *purgeAttemptExecV1
	sealed, closing, closed bool
}

func newPurgeAttemptCodeV2(ctx context.Context, origin *placement, args api.PurgeArgs) (result *purgeAttemptCodeV2, returned error) {
	if ctx == nil || origin == nil || !ValidHostID(args.HostID) || !ValidHostID(args.Nonce) || origin.hostID != args.HostID {
		return nil, sessionLeaseError("the temporary purge code has no original authorised host and attempt")
	}
	if err := errors.Join(ctx.Err(), CheckCaller(), origin.checkIdentity()); err != nil {
		return nil, err
	}
	owner := &purgeAttemptCodeV2{origin: origin, directoryLeaf: "." + brand.Name + "-attempt-" + args.Nonce, copyLeaf: brand.HostBinary, leaseLeaf: ".code.lock", loans: map[*purgeAttemptCodeLoanV2]struct{}{}}
	owner.directoryCapture = owner.directoryLeaf + ".closing"
	complete := false
	defer func() {
		if !complete {
			closeErr := owner.Close()
			returned = errors.Join(returned, closeErr)
			if closeErr != nil {
				// The caller adopts this SAME real owner before handling the error.
				// A failed cleanup never becomes an orphaned path-only receipt.
				result = owner
			}
		}
	}()
	var err error
	owner.source, err = os.Open("/proc/self/exe")
	if err != nil {
		return nil, err
	}
	owner.sourceInfo, err = owner.source.Stat()
	if err != nil || !os.SameFile(owner.sourceInfo, origin.executableRef) || owner.sourceInfo.Size() < 1 || owner.sourceInfo.Size() > 256<<20 {
		return nil, errors.Join(sessionLeaseError("the actual mapped purge code source changed or exceeds its bound"), err)
	}
	if err := checkPurgeKernelExecEligibilityV2(ctx, owner.source); err != nil {
		return nil, err
	}
	if err := withPlacementHome(ctx, func(home *os.Root) error {
		var err error
		owner.home, err = home.OpenRoot(".")
		if err != nil {
			return err
		}
		owner.homeRef, err = nativeWorktreeDirectoryRefV1(owner.home)
		if err != nil {
			return err
		}
		for _, root := range []string{origin.boundRoots.State, origin.boundRoots.Data, origin.boundRoots.Config} {
			relative, err := filepath.Rel(root, filepath.Join(owner.home.Name(), owner.directoryLeaf))
			if err != nil || relative == "." || filepath.IsLocal(relative) {
				return sessionLeaseError("the temporary purge code would lie inside a removable root")
			}
		}
		// Existing named data cannot reconstruct an actual prior code factory.
		if err := owner.home.Mkdir(owner.directoryLeaf, 0o700); err != nil {
			return err
		}
		info, err := owner.home.Lstat(owner.directoryLeaf)
		if err != nil || !refOf(info).validDirectory() {
			return errors.Join(sessionLeaseError("the original temporary code directory lost its native creation"), err)
		}
		owner.directoryRef = refOf(info)
		owner.directory, err = owner.home.OpenRoot(owner.directoryLeaf)
		if err != nil {
			return err
		}
		return errors.Join(owner.directoryRef.matchesRoot(owner.directory), cachefs.ValidateRoot(owner.directory))
	}); err != nil {
		return nil, err
	}
	owner.lease, err = cachefs.OpenFile(owner.directory, owner.leaseLeaf, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	owner.leaseInfo, err = owner.lease.Stat()
	if err != nil {
		return nil, err
	}
	owner.leaseCreated, err = cachefs.CaptureFile(owner.directory, owner.leaseLeaf, owner.lease, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(owner.lease.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil, err
	}
	owner.copy, err = cachefs.OpenFile(owner.directory, owner.copyLeaf, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	owner.copyInfo, err = owner.copy.Stat()
	if err != nil {
		return nil, err
	}
	owner.copyCreated, err = cachefs.CaptureFile(owner.directory, owner.copyLeaf, owner.copy, 0o600)
	if err != nil {
		return nil, err
	}
	n, err := io.Copy(owner.copy, io.LimitReader(sessionOperationReader{ctx: ctx, reader: owner.source}, owner.sourceInfo.Size()+1))
	if err != nil || n != owner.sourceInfo.Size() {
		return nil, errors.Join(sessionLeaseError("the original mapped code did not copy exactly"), err)
	}
	if err := verifyOriginalPurgeCodeBytesV2(ctx, owner.source, owner.copy, owner.sourceInfo); err != nil {
		return nil, err
	}
	if err := owner.copy.Sync(); err != nil {
		return nil, err
	}
	// Open the read-only descriptor BEFORE making the named image unreadable.
	// The raw child will bind this exact inherited FD to its kernel mapping.
	readOnly, err := cachefs.OpenFile(owner.directory, owner.copyLeaf, os.O_RDONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err := owner.copy.Close(); err != nil {
		_ = readOnly.Close()
		return nil, err
	}
	owner.copy = readOnly
	if err := owner.copy.Chmod(0o100); err != nil {
		return nil, err
	}
	owner.copyReference, err = cachefs.CaptureFile(owner.directory, owner.copyLeaf, owner.copy, 0o100)
	if err != nil {
		return nil, err
	}
	if err := errors.Join(owner.lease.Sync(), owner.copy.Sync(), syncCapturedDirectory(owner.directory), origin.checkIdentity()); err != nil {
		return nil, err
	}
	owner.sealed = true
	if err := owner.checkLockedV2(ctx); err != nil {
		return nil, err
	}
	complete = true
	return owner, nil
}

func verifyOriginalPurgeCodeBytesV2(ctx context.Context, original, copied *os.File, before os.FileInfo) error {
	if ctx == nil || original == nil || copied == nil || before == nil || before.Size() < 1 || before.Size() > 256<<20 {
		return sessionLeaseError("the native code comparison has no original descriptor and fixed bound")
	}
	originalInfo, err := original.Stat()
	copyInfo, copyErr := copied.Stat()
	if err != nil || copyErr != nil || !os.SameFile(originalInfo, before) || originalInfo.Size() != before.Size() || !originalInfo.ModTime().Equal(before.ModTime()) || copyInfo.Size() != before.Size() {
		return errors.Join(sessionLeaseError("the original code descriptors changed before exact comparison"), err, copyErr)
	}
	a, b := make([]byte, 64<<10), make([]byte, 64<<10)
	for offset := int64(0); offset < before.Size(); {
		if err := ctx.Err(); err != nil {
			return err
		}
		length := min(int64(len(a)), before.Size()-offset)
		n, err := original.ReadAt(a[:length], offset)
		m, copyErr := copied.ReadAt(b[:length], offset)
		if err != nil && !errors.Is(err, io.EOF) || copyErr != nil && !errors.Is(copyErr, io.EOF) || n != int(length) || m != n || !bytes.Equal(a[:n], b[:m]) {
			return errors.Join(sessionLeaseError("the actual temporary code differs from its retained mapped original"), err, copyErr)
		}
		offset += length
	}
	after, err := original.Stat()
	copyAfter, copyErr := copied.Stat()
	if err != nil || copyErr != nil || !os.SameFile(after, before) || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) || !os.SameFile(copyAfter, copyInfo) || copyAfter.Size() != copyInfo.Size() || !copyAfter.ModTime().Equal(copyInfo.ModTime()) {
		return errors.Join(sessionLeaseError("the actual code changed during descriptor comparison"), err, copyErr)
	}
	return nil
}

func (owner *purgeAttemptCodeV2) checkLockedV2(ctx context.Context) error {
	if owner == nil || ctx == nil || !owner.sealed || owner.closing || owner.closed || owner.home == nil || owner.directory == nil || owner.source == nil || owner.copy == nil || owner.lease == nil {
		return sessionLeaseError("the original temporary purge code owner is absent or closed")
	}
	if err := errors.Join(ctx.Err(), owner.homeRef.matchesRoot(owner.home), owner.directoryRef.matchesRoot(owner.directory), owner.copyReference.CheckFile(owner.directory, owner.copyLeaf, owner.copy, 0o100), owner.leaseCreated.CheckFile(owner.directory, owner.leaseLeaf, owner.lease, 0o600), checkOrdinaryHolderFlockV1(owner.lease)); err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	fresh, err := cachefs.OpenSafeAncestor(home)
	if err != nil {
		return err
	}
	if err := errors.Join(owner.homeRef.matchesRoot(fresh), fresh.Close()); err != nil {
		return err
	}
	current, err := owner.home.Lstat(owner.directoryLeaf)
	if err != nil || !owner.directoryRef.matches(current) {
		return errors.Join(sessionLeaseError("the original temporary code directory was replaced"), err)
	}
	return verifyOriginalPurgeCodeBytesV2(ctx, owner.source, owner.copy, owner.sourceInfo)
}

// Check repeats the real code/lease/root checks. Its sealed field cannot
// substitute for actual original descriptors, bytes and the retained lock OFD.
func (owner *purgeAttemptCodeV2) Check(ctx context.Context) error {
	if owner == nil {
		return sessionLeaseError("the original temporary purge code owner is absent")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	return owner.checkLockedV2(ctx)
}

// These are public native bootstrap roles, not OP or session Source loans.
// Their original code factory must still bind a real child before private use.
type purgeAttemptCodeLoanV2 struct {
	creator                                       *purgeAttemptCodeV2
	OriginalCode, CopiedCode, ParentPidfd, Cancel *os.File
	once                                          sync.Once
	closeErr                                      error
}

func (owner *purgeAttemptCodeV2) borrowForOriginalExecV1(ctx context.Context) (result *purgeAttemptCodeLoanV2, returned error) {
	if owner == nil {
		return nil, sessionLeaseError("the native code bootstrap has no actual copy factory")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if err := owner.checkLockedV2(ctx); err != nil {
		return nil, err
	}
	loan := &purgeAttemptCodeLoanV2{creator: owner}
	owner.loans[loan] = struct{}{}
	complete := false
	defer func() {
		if !complete {
			var closeErr error
			for _, file := range []*os.File{loan.OriginalCode, loan.CopiedCode, loan.ParentPidfd, loan.Cancel} {
				if file != nil {
					if err := file.Close(); !errors.Is(err, os.ErrClosed) {
						closeErr = errors.Join(closeErr, err)
					}
				}
			}
			returned = errors.Join(returned, closeErr)
			if closeErr != nil {
				result = loan
			} else {
				delete(owner.loans, loan)
			}
		}
	}()
	for _, pair := range []struct {
		source *os.File
		target **os.File
	}{{owner.source, &loan.OriginalCode}, {owner.copy, &loan.CopiedCode}} {
		fd, err := unix.FcntlInt(pair.source.Fd(), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			return nil, err
		}
		*pair.target = os.NewFile(uintptr(fd), "actual-original-purge-code-bootstrap")
	}
	fd, err := unix.PidfdOpen(os.Getpid(), 0) // This running creator itself, never a saved or observed foreign PID.
	if err != nil {
		return nil, err
	}
	loan.ParentPidfd = os.NewFile(uintptr(fd), "actual-code-creator-self-pidfd")
	cancelFD, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		return nil, err
	}
	loan.Cancel = os.NewFile(uintptr(cancelFD), "actual-original-purge-request-cancellation")
	complete = true
	return loan, nil
}

func (loan *purgeAttemptCodeLoanV2) Close() error {
	if loan == nil {
		return nil
	}
	loan.once.Do(func() {
		for _, file := range []*os.File{loan.OriginalCode, loan.CopiedCode, loan.ParentPidfd, loan.Cancel} {
			if file != nil {
				if err := file.Close(); !errors.Is(err, os.ErrClosed) {
					loan.closeErr = errors.Join(loan.closeErr, err)
				}
			}
		}
		if loan.creator != nil {
			loan.creator.mu.Lock()
			delete(loan.creator.loans, loan)
			loan.creator.mu.Unlock()
		}
	})
	return loan.closeErr
}
