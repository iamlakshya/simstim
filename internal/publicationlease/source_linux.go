//go:build linux

package publicationlease

import (
	"context"
	"errors"
	"os"
	"sync"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"golang.org/x/sys/unix"
)

// SourceV1 owns the original parent RO shared OFD. Only exclusive creation
// under the actual operation owner's held control root can construct it.
// It is a retention capability, never an operation or execution grant.
type SourceV1 struct {
	mu                                    sync.Mutex
	root                                  *os.Root
	lock                                  *os.File
	record                                RecordV1
	encoded                               string
	pid, uid                              int
	frozen                                bool
	proof                                 *ExclusiveV1
	writer                                *os.File
	failed, anonymous                     bool
	loans                                 uint64
	unpublishedRemoved, unpublishedSynced bool
}

// RecordV1 returns immutable correlation data for the existing operation.
// It does not lend a descriptor or establish vacancy.
func (source *SourceV1) RecordV1() string {
	if source == nil {
		return ""
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	return source.encoded
}
func (source *SourceV1) checkV1(ctx context.Context) error {
	if source == nil || source.root == nil || source.lock == nil || source.frozen || source.failed || source.pid != os.Getpid() || source.uid != os.Getuid() {
		return failureV1("the publication has no original current native source")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := source.record.Root.CheckRoot(source.root); err != nil {
		return err
	}
	if err := source.record.Self.CheckCreatedFile(source.root, nameV1(source.record.Token), source.lock, 0o600); err != nil {
		return err
	}
	_, encoded, err := readRecordV1(source.lock)
	if err != nil {
		return err
	}
	if encoded != source.encoded {
		return failureV1("the native publication fence changed its original record")
	}
	return checkReadonlyV1(source.lock, source.record)
}

// RetainForHelperV1 duplicates the SAME original RO shared OFD. It never
// reopens the file or lends the control directory or a writable descriptor.
func (source *SourceV1) RetainForHelperV1(ctx context.Context) (*os.File, error) {
	if source == nil {
		return nil, failureV1("the original publication source is missing")
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if err := source.checkV1(ctx); err != nil {
		return nil, err
	}
	file, err := duplicateV1(source.lock, "original-publication-fence")
	if err == nil {
		source.loans++
	}
	return file, err
}

// FreezeParentV1 irrevocably blocks new helper loans and drops only the
// parent's SH copy. Every existing helper keeps its original inherited OFD.
// This provides NO vacancy proof. The canonical operation must stay reserved.
func (source *SourceV1) FreezeParentV1() error {
	if source == nil {
		return failureV1("the original publication source is missing")
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	source.frozen = true
	if source.lock == nil {
		return nil
	}
	err := source.lock.Close()
	source.lock = nil
	return err
}

// ProbeV1 obtains a fresh native EX lock only after ALL inherited SH copies
// are gone. A busy result never waits under an operation/cache transaction.
// Parent liveness may reject ledger cleanup; this proof covers helpers only.
func (source *SourceV1) ProbeV1(ctx context.Context) (*ExclusiveV1, error) {
	if source == nil {
		return nil, failureV1("the original publication source is missing")
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if !source.frozen || source.lock != nil {
		return nil, failureV1("the original parent still permits future publication helpers")
	}
	if source.proof != nil {
		return source.proof, nil
	}
	proof, err := TryExclusiveV1(ctx, source.root, source.record.Domain, source.encoded)
	if err != nil {
		return nil, err
	}
	source.proof = proof
	return proof, nil
}

// CloseV1 releases metadata only after the caller has frozen and proved the
// helper fence. A live original fence is retained on uncertainty.
func (source *SourceV1) CloseV1() error {
	if source == nil {
		return nil
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.root == nil {
		return nil
	}
	unpublished := source.failed && source.loans == 0 && source.unpublishedSynced
	if !unpublished && (!source.frozen || source.lock != nil || source.proof == nil) {
		return ErrBusy
	}
	var err error
	if source.proof != nil {
		err = source.proof.CloseV1()
		source.proof = nil
	}
	for _, file := range []*os.File{source.lock, source.writer} {
		if file != nil {
			err = errors.Join(err, file.Close())
		}
	}
	source.lock, source.writer = nil, nil
	if source.root != nil {
		err = errors.Join(err, source.root.Close())
		source.root = nil
	}
	return err
}
func duplicateV1(file *os.File, name string) (*os.File, error) {
	if file == nil {
		return nil, failureV1("the original publication file is missing")
	}
	raw, err := file.SyscallConn()
	if err != nil {
		return nil, err
	}
	fd := -1
	var nativeErr error
	err = raw.Control(func(original uintptr) { fd, nativeErr = unix.FcntlInt(original, unix.F_DUPFD_CLOEXEC, 3) })
	if err != nil || nativeErr != nil {
		if fd >= 0 {
			unix.Close(fd)
		}
		return nil, errors.Join(err, nativeErr)
	}
	return os.NewFile(uintptr(fd), name), nil
}

// CheckV1 verifies only this actual current parent source and immutable
// native sidecar. The operation owner separately checks its canonical record.
func (source *SourceV1) CheckV1(ctx context.Context) error {
	if source == nil {
		return failureV1("the original publication source is missing")
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	return source.checkV1(ctx)
}

// CloseUnpublishedV1 cleans only an actual failed factory with NO helper
// loans. It uses the retained original creator descriptor and native refs;
// unknown identities remain held. It never handles a possibly published loan.
func (source *SourceV1) CloseUnpublishedV1() error {
	if source == nil {
		return failureV1("the unpublished source is missing")
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if !source.failed || source.loans != 0 || source.pid != os.Getpid() || source.uid != os.Getuid() {
		return failureV1("the failed publication is not its original unpublished creator")
	}
	if source.unpublishedSynced {
		return nil
	}
	if source.root == nil {
		return failureV1("the unpublished creator lost its original root")
	}
	if !source.unpublishedRemoved {
		if source.anonymous {
			actual, err := unlinkedReferenceV1(source.writer)
			if err != nil {
				return err
			}
			if source.record.Self.Known() && actual.Identity != source.record.Self.Identity {
				return failureV1("the unpublished fence changed its original unnamed inode")
			}
			source.unpublishedRemoved, source.frozen = true, true
		}
	}
	if !source.unpublishedRemoved {
		if !source.record.Root.Known() || !source.record.Self.Known() {
			return failureV1("the failed publication has no complete native creator identity")
		}
		if err := source.record.Root.CheckRoot(source.root); err != nil {
			return err
		}
		creator := source.writer
		if creator == nil {
			creator = source.lock
		}
		if creator == nil {
			return failureV1("the failed publication lost its original creator descriptor")
		}
		if err := source.record.Self.CheckCreatedFile(source.root, nameV1(source.record.Token), creator, 0o600); err != nil {
			return err
		}
		if err := source.root.Remove(nameV1(source.record.Token)); err != nil {
			return err
		}
		source.unpublishedRemoved = true
		source.frozen = true
	}
	directory, err := source.root.Open(".")
	if err != nil {
		return err
	}
	err = errors.Join(cachefs.SyncDirectory(directory), directory.Close())
	if err == nil {
		source.unpublishedSynced = true
	}
	// Keep exact metadata handles until the canonical Active write also
	// succeeds. CloseV1 is the separate final resource step, retryable below.
	return err
}

// FileInfoV1 lends a current native observation for the owner's EXISTING
// footprint-role receipt. The observation alone grants no removal authority.
func (source *SourceV1) FileInfoV1() (os.FileInfo, error) {
	if source == nil {
		return nil, failureV1("the original publication source is missing")
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.lock == nil || source.frozen || source.failed {
		return nil, failureV1("the original publication sidecar is not a current created source")
	}
	return source.lock.Stat()
}
