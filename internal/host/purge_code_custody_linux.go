//go:build linux

package host

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/proto"
)

// This is only the original temporary factory's native code-artefact loan.
// The child never derives session, root or OP authority from these objects.
type purgeCodeCustodyV3 struct {
	mu          sync.Mutex
	image       *purgeAttemptImageV2
	root        *os.Root
	rootRef     cachefs.Reference
	rootInfo    os.FileInfo
	home        *os.Root
	homeRef     purgeRef
	leaf        string
	capture     string
	codeLeaf    string
	codeInfo    os.FileInfo
	codeRef     cachefs.Reference
	lease       *os.File
	leaseInfo   os.FileInfo
	leaseRef    cachefs.Reference
	leaseLeaf   string
	endpoint    *purgeOwnedListenerV3
	transaction *purgeActiveTransactionV2
	closed      bool
	closeErr    error
}

func receivePurgeCodeCustodyV3(ctx context.Context, image *purgeAttemptImageV2, line *StartupLineV1, peer *purgeRequestPeerV3, original proto.Request, first purgeRequestDataV3) (result *purgeCodeCustodyV3, returned error) {
	if err := image.Check(ctx); err != nil {
		return nil, err
	}
	owner := &purgeCodeCustodyV3{image: image, codeLeaf: brand.HostBinary, leaseLeaf: ".code.lock"}
	complete := false
	defer func() {
		if !complete {
			closeErr := owner.closeBorrowedV3()
			returned = errors.Join(returned, closeErr)
			if closeErr != nil {
				result = owner
			}
		}
	}()
	var directory *os.File
	defer func() {
		if directory != nil {
			returned = errors.Join(returned, directory.Close())
		}
	}()
	for index := 0; index < 2; index++ {
		request, data, err := readPurgeRequestV3(ctx, line, peer)
		if err != nil || request.Op != purgeBeginV3 || request.ID != original.ID || data != first {
			return nil, errors.Join(placementError("the temporary factory code loan changed its original continuation"), err)
		}
		file, sender, err := line.takeLoanFile()
		if err != nil {
			return nil, err
		}
		if index == 0 {
			directory = file
		} else {
			owner.lease = file
		}
		if err := image.checkOriginalOperatorParentV3(ctx, peer, sender); err != nil {
			return nil, err
		}
	}
	info, err := directory.Stat()
	if err != nil || !info.IsDir() || !refOf(info).validDirectory() {
		return nil, errors.Join(placementError("the factory code directory loan has another native role"), err)
	}
	owner.root, err = os.OpenRoot("/proc/self/fd/" + strconv.FormatUint(uint64(directory.Fd()), 10))
	if err != nil {
		return nil, err
	}
	owner.rootRef, err = cachefs.CaptureRoot(owner.root)
	if err != nil {
		return nil, err
	}
	owner.rootInfo = info
	current, err := os.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	matched := owner.rootRef.CheckRoot(current)
	closed := current.Close()
	if err := errors.Join(matched, closed); err != nil {
		return nil, err
	}
	owner.codeInfo, err = cachefs.ValidateFile(owner.root, owner.codeLeaf, image.copied, 0o100)
	if err != nil || !os.SameFile(owner.codeInfo, image.mapped) {
		return nil, errors.Join(placementError("the factory code root does not contain this actual protected mapped image"), err)
	}
	owner.codeRef, err = cachefs.CaptureFile(owner.root, owner.codeLeaf, image.copied, 0o100)
	if err != nil {
		return nil, err
	}
	owner.leaseInfo, err = cachefs.ValidateFile(owner.root, owner.leaseLeaf, owner.lease, 0o600)
	if err != nil {
		return nil, err
	}
	owner.leaseRef, err = cachefs.CaptureFile(owner.root, owner.leaseLeaf, owner.lease, 0o600)
	if err != nil {
		return nil, err
	}
	if err := checkOrdinaryHolderFlockV1(owner.lease); err != nil {
		return nil, err
	}
	name, err := os.Executable()
	if err != nil {
		return nil, err
	}
	directoryPath := filepath.Dir(name)
	owner.leaf = filepath.Base(directoryPath)
	owner.capture = owner.leaf + ".closing"
	owner.home, err = cachefs.OpenSafeAncestor(filepath.Dir(directoryPath))
	if err != nil {
		return nil, err
	}
	owner.homeRef, err = nativeWorktreeDirectoryRefV1(owner.home)
	if err != nil || !purgeSafeParentRefV1(owner.homeRef) {
		return nil, errors.Join(placementError("the actual code factory parent has unknown native identity"), err)
	}
	actual, err := owner.home.Lstat(owner.leaf)
	if err != nil || !os.SameFile(actual, owner.rootInfo) {
		return nil, errors.Join(placementError("the actual code directory changed its native parent binding"), err)
	}
	complete = true
	return owner, nil
}

func (owner *purgeCodeCustodyV3) closeBorrowedV3() error {
	if owner == nil {
		return nil
	}
	var failure error
	if owner.lease != nil {
		failure = errors.Join(failure, owner.lease.Close())
	}
	if owner.root != nil {
		failure = errors.Join(failure, owner.root.Close())
	}
	if owner.home != nil {
		failure = errors.Join(failure, owner.home.Close())
	}
	return failure
}

// CloseAfterTransaction removes only the actual original factory code objects,
// after the same transaction has closed its verified physical subjects.
func (owner *purgeCodeCustodyV3) CloseAfterTransaction(transaction *purgeActiveTransactionV2) error {
	if owner == nil {
		return nil
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed {
		if err := observeOriginalPurgeDescriptorCloseV3([]*os.File{owner.lease}, []*os.Root{owner.root, owner.home}); err != nil {
			return err
		}
		return nil
	}
	if transaction == nil || owner.transaction != transaction || !transaction.closed {
		return placementError("the actual code custody still has its original transaction")
	}
	if owner.root == nil || owner.home == nil || owner.lease == nil || owner.image == nil {
		return placementError("the actual factory code custody is incomplete")
	}
	directory, err := owner.root.Open(".")
	if err != nil {
		return err
	}
	actual, statErr := directory.Stat()
	closedErr := directory.Close()
	_, _, _, links, known := footprintIdentity(actual)
	if statErr != nil || closedErr != nil || !known || !os.SameFile(actual, owner.rootInfo) {
		return errors.Join(placementError("the original code directory lost its retained native identity"), statErr, closedErr)
	}
	if links == 0 {
		// Only SAME retained creation handles can prove a previous physical
		// unlink. A replacement/missing named directory supplies no fact.
		for _, file := range []*os.File{owner.image.copied, owner.lease} {
			info, err := file.Stat()
			_, _, _, fileLinks, fileKnown := footprintIdentity(info)
			if err != nil || !fileKnown || fileLinks != 0 {
				return errors.Join(placementError("the terminal code directory still has an unknown original file"), err)
			}
		}
		if owner.endpoint == nil || !owner.endpoint.closed {
			return placementError("the unlinked code directory still has an original listener")
		}
		if err := syncCapturedDirectory(owner.home); err != nil {
			return err
		}
		owner.closeErr = owner.closeBorrowedV3()
		owner.closed = true
		return owner.closeErr
	}
	if err := owner.rootRef.CheckRoot(owner.root); err != nil {
		return err
	}
	if err := owner.homeRef.matchesRoot(owner.home); err != nil {
		return err
	}
	if err := checkOrdinaryHolderFlockV1(owner.lease); err != nil {
		return err
	}
	file, err := owner.root.Open(".")
	if err != nil {
		return err
	}
	entries, err := file.ReadDir(3)
	closed := file.Close()
	if err != nil && !errors.Is(err, io.EOF) || closed != nil {
		return errors.Join(err, closed)
	}
	for _, entry := range entries {
		if entry.Name() != owner.codeLeaf && entry.Name() != owner.leaseLeaf && !(entry.Name() == "attempt.sock" && owner.endpoint != nil) {
			return placementError("the temporary factory root retains an unrelated entry")
		}
	}
	if err := removeOwnPurgeCodeFileV2(owner.root, owner.image.copied, owner.codeInfo, owner.codeRef, &owner.codeLeaf, ".remove-code"); err != nil {
		return err
	}
	if err := removeOwnPurgeCodeFileV2(owner.root, owner.lease, owner.leaseInfo, owner.leaseRef, &owner.leaseLeaf, ".remove-lock"); err != nil {
		return err
	}
	if owner.endpoint != nil {
		if err := owner.endpoint.Close(); err != nil {
			return err
		}
	}
	if owner.leaf != owner.capture {
		current, err := owner.home.Lstat(owner.leaf)
		if err != nil || !os.SameFile(current, owner.rootInfo) {
			return errors.Join(placementError("the actual code directory was replaced"), err)
		}
		if err := cachefs.RenameOwnedDirectoryNoReplace(owner.home, owner.leaf, owner.capture); err != nil {
			return err
		}
		current, err = owner.home.Lstat(owner.capture)
		if err != nil || !os.SameFile(current, owner.rootInfo) {
			return errors.Join(placementError("preserved a changed factory directory capture"), err, cachefs.RenameNoReplace(owner.home, owner.capture, owner.leaf))
		}
		owner.leaf = owner.capture
	}
	if err := owner.home.Remove(owner.leaf); err != nil {
		return err
	}
	if err := syncCapturedDirectory(owner.home); err != nil {
		return err
	}
	owner.closeErr = owner.closeBorrowedV3()
	owner.closed = true
	return owner.closeErr
}
