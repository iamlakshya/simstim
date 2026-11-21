//go:build linux

package publicationlease

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"golang.org/x/sys/unix"
)

// CreateV1 is the native factory for callers that own their surrounding
// transaction. Production uses CreateReservedV1 to persist before linking.
func CreateV1(ctx context.Context, root *os.Root, domain cachefs.Domain, token string) (*SourceV1, error) {
	return CreateReservedV1(ctx, root, domain, token, nil)
}

// CreateReservedV1 creates an unnamed native inode, then calls reserve with
// its exact root/self identity BEFORE making any name or helper observable.
// The caller holds its existing canonical control transaction and persists
// that identity as a creating operation. It publishes readiness only after
// this function returns successfully. A crash leaves either no named inode
// or the complete checked record; no helper may exist in the creating phase.
// A post-create failure returns the SAME retained source for cleanup retry.
func CreateReservedV1(ctx context.Context, root *os.Root, domain cachefs.Domain, token string, reserve func(string) error) (retained *SourceV1, returned error) {
	if ctx == nil || root == nil || !tokenV1(token) || !domain.Known() {
		return nil, failureV1("the publication has no original control root, token or domain")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	held, err := root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	source := &SourceV1{root: held, pid: os.Getpid(), uid: os.Getuid()}
	defer func() {
		if returned == nil {
			return
		}
		if source.writer != nil || source.lock != nil {
			source.failed = true
			retained = source
			return
		}
		if source.lock != nil {
			returned = errors.Join(returned, source.lock.Close())
		}
		returned = errors.Join(returned, held.Close())
	}()
	source.record = RecordV1{Version: 1, Role: roleV1, Token: token, Domain: domain}
	source.record.Root, err = cachefs.CaptureRoot(held)
	if err != nil {
		return nil, err
	}
	directory, err := held.Open(".")
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	fd, err := unix.Openat(int(directory.Fd()), ".", unix.O_TMPFILE|unix.O_RDWR|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create unnamed publication fence: %w", err)
	}
	source.writer = os.NewFile(uintptr(fd), "original-unpublished-fence")
	source.anonymous = true
	source.record.Self, err = unlinkedReferenceV1(source.writer)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(source.record)
	if err != nil {
		return nil, err
	}
	source.encoded = string(data)
	if reserve != nil {
		if err := reserve(source.encoded); err != nil {
			return nil, err
		}
	}
	if _, err := source.writer.Write(append(data, '\n')); err != nil {
		return nil, err
	}
	if err := source.writer.Sync(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// /proc/self/fd follows this held unnamed inode, not a store pathname.
	// Unlike linkat(AT_EMPTY_PATH), this works without CAP_DAC_READ_SEARCH.
	var fs unix.Statfs_t
	if err := unix.Statfs("/proc/self/fd", &fs); err != nil {
		return nil, err
	}
	if fs.Type != unix.PROC_SUPER_MAGIC {
		return nil, failureV1("the native fence link source is not procfs")
	}
	if err := unix.Linkat(unix.AT_FDCWD, "/proc/self/fd/"+strconv.Itoa(fd), int(directory.Fd()), nameV1(token), unix.AT_SYMLINK_FOLLOW); err != nil {
		return nil, err
	}
	source.anonymous = false
	if err := source.record.Self.CheckCreatedFile(held, nameV1(token), source.writer, 0o600); err != nil {
		return nil, err
	}
	if err := cachefs.SyncDirectory(directory); err != nil {
		return nil, err
	}
	source.lock, err = cachefs.OpenFile(held, nameV1(token), os.O_RDONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err := source.record.Self.CheckCreatedFile(held, nameV1(token), source.lock, 0o600); err != nil {
		return nil, err
	}
	locked, err := cachefs.TryLock(source.lock, cachefs.Shared)
	if err != nil {
		return nil, err
	}
	if !locked {
		return nil, ErrBusy
	}
	if err := source.checkV1(ctx); err != nil {
		return nil, err
	}
	if err := source.writer.Close(); err != nil {
		source.writer = nil
		return nil, err
	}
	source.writer = nil
	return source, nil
}

func unlinkedReferenceV1(file *os.File) (cachefs.Reference, error) {
	info, err := file.Stat()
	if err != nil {
		return cachefs.Reference{}, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return cachefs.Reference{}, err
	}
	if !info.Mode().IsRegular() || info.Mode() != 0o600 || stat.Nlink != 0 || stat.Uid != uint32(os.Getuid()) {
		return cachefs.Reference{}, failureV1("the unpublished fence is not its original unnamed private inode")
	}
	return cachefs.Reference{Version: 1, Identity: fmt.Sprintf("unix:%v:%v:%v", stat.Dev, stat.Ino, stat.Uid), Mode: 0o600, Size: info.Size(), Modified: info.ModTime()}, nil
}
