//go:build linux

package publicationlease

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

// HolderV1 is captured by the fixed guardian entry from its original native
// handoff. It can retain the SAME RO SH OFD for its concrete converter Cmd;
// it grants no new ordinary data-file permissions or ledger authority.
type HolderV1 struct {
	mu       sync.Mutex
	file     *os.File
	encoded  string
	record   RecordV1
	pid, uid int
	closed   bool
}

// CaptureGuardianV1 consumes only fixed private FD200. The actual parent
// producer selects a distinct closed entry and populated this exact slot via
// its real ExtraFiles. A saved record or a slot number alone issues nothing.
func CaptureGuardianV1() (*HolderV1, error) {
	fd, err := unix.FcntlInt(GuardianFDV1, unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "original-publication-guardian-fence")
	record, encoded, err := readRecordV1(file)
	if err != nil {
		file.Close()
		return nil, err
	}
	if err := checkReadonlyV1(file, record); err != nil {
		file.Close()
		return nil, err
	}
	// Only after a complete native-role check may the fixed original slot be
	// owned/closed. A malformed invocation leaves an unknown FD200 untouched.
	original := os.NewFile(GuardianFDV1, "original-publication-guardian-handoff")
	if err := original.Close(); err != nil {
		file.Close()
		return nil, err
	}
	holder := &HolderV1{file: file, encoded: encoded, record: record, pid: os.Getpid(), uid: os.Getuid()}
	if err := holder.checkV1(context.Background()); err != nil {
		file.Close()
		return nil, err
	}
	return holder, nil
}
func (holder *HolderV1) checkV1(ctx context.Context) error {
	if holder == nil || holder.closed || holder.file == nil || holder.pid != os.Getpid() || holder.uid != os.Getuid() {
		return failureV1("the guardian has no original current publication holder")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkReadonlyV1(holder.file, holder.record); err != nil {
		return err
	}
	_, encoded, err := readRecordV1(holder.file)
	if err != nil {
		return err
	}
	if encoded != holder.encoded {
		return failureV1("the inherited publication fence changed its original record")
	}
	return nil
}

// RetainForJailV1 lends a duplicate of the SAME inherited original SH OFD.
// The concrete private jail constructor still checks its exact converter role.
func (holder *HolderV1) RetainForJailV1(ctx context.Context) (*os.File, string, error) {
	if holder == nil {
		return nil, "", failureV1("the original guardian publication holder is missing")
	}
	holder.mu.Lock()
	defer holder.mu.Unlock()
	if err := holder.checkV1(ctx); err != nil {
		return nil, "", err
	}
	file, err := duplicateV1(holder.file, "original-publication-jail-fence")
	return file, holder.encoded, err
}

// CloseV1 drops this trusted guardian's reference after its actual converter
// source/join has completed. Surviving helpers retain their OWN original copies.
func (holder *HolderV1) CloseV1() error {
	if holder == nil {
		return nil
	}
	holder.mu.Lock()
	defer holder.mu.Unlock()
	if holder.closed {
		return nil
	}
	holder.closed = true
	return holder.file.Close()
}

// CheckInheritedV1 checks DATA roles only. Distinct private jail dispatch and
// the concrete retained Source/Holder establish the actual inheritance purpose.
func CheckInheritedV1(file *os.File, encoded string) error {
	record, err := DecodeRecordV1(encoded)
	if err != nil {
		return err
	}
	if err := checkReadonlyV1(file, record); err != nil {
		return err
	}
	_, actual, err := readRecordV1(file)
	if err != nil {
		return err
	}
	if actual != encoded {
		return failureV1("the projected fence differs from its original operation")
	}
	return nil
}
func checkReadonlyV1(file *os.File, record RecordV1) error {
	if file == nil {
		return failureV1("the original inherited publication fence is missing")
	}
	if err := record.checkV1(); err != nil {
		return err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return err
	}
	flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
	if err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o7777 != 0o600 || stat.Nlink != 1 || flags&unix.O_ACCMODE != unix.O_RDONLY || flags&unix.O_PATH != 0 {
		return failureV1("the publication holder lost its original readonly native role")
	}
	parts := strings.Split(record.Self.Identity, ":")
	if len(parts) != 4 || parts[0] != "unix" {
		return failureV1("the publication holder has no original native file identity")
	}
	dev, e1 := strconv.ParseUint(parts[1], 10, 64)
	ino, e2 := strconv.ParseUint(parts[2], 10, 64)
	_, e3 := strconv.ParseUint(parts[3], 10, 32)
	// The canonical owner was checked before namespace mapping. Here the same
	// inherited inode can have a mapped UID; no mapped UID creates a new grant.
	if e1 != nil || e2 != nil || e3 != nil || dev != uint64(stat.Dev) || ino != uint64(stat.Ino) {
		return failureV1("the publication holder substituted another native inode")
	}
	// The actual Source acquired SH before lending this SAME OFD. A data
	// check must never call flock: that could silently reacquire a lost lock
	// or downgrade another description supplied by an unrelated invocation.
	return nil
}

// CheckConverterFDV1 checks the fixed inherited worker slot without adopting
// or closing it. Its concrete private command selects retention, not authority.
func CheckConverterFDV1(fd int) error {
	owned, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(owned), "checked-publication-worker-fence")
	defer file.Close()
	record, _, err := readRecordV1(file)
	if err != nil {
		return err
	}
	return checkReadonlyV1(file, record)
}

// CheckInheritedFDV1 borrows a stage's raw private slot through an owned dup.
// It never creates a finaliser for or closes an unknown original descriptor.
func CheckInheritedFDV1(fd int, encoded string) error {
	owned, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(owned), "checked-publication-private-fence")
	defer file.Close()
	return CheckInheritedV1(file, encoded)
}
