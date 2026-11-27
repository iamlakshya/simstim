//go:build linux

package filecopy

import (
	"errors"
	"os"
	"strconv"
	"sync"

	"golang.org/x/sys/unix"
)

// errStageCommitOwner is an internal continuation request, not authority.
// The existing receiver must obtain its original trusted commit owner for
// replacement or a filesystem without anonymous publication. It must never
// substitute a named temporary file in the public destination directory.
var errStageCommitOwner = errors.New("copy needs its original publication owner")

type stageCommitError struct{ cause error }

func (e *stageCommitError) Error() string        { return e.cause.Error() }
func (e *stageCommitError) Unwrap() error        { return e.cause }
func (e *stageCommitError) Is(target error) bool { return target == errStageCommitOwner }

// newStage retains its own native parent and anonymous file. The caller has
// already checked the actual receiver account, rooted path and transfer cap.
// It writes, checks, Syncs and Chmods 0644, then publishes with this file LIVE.
// Cleanup alone closes the stage; neither failure nor cancellation unlinks a
// public name. Publish is one-shot, including an ambiguous filesystem error.
func newStage(parent *os.Root) (*os.File, func(string, bool) error, func() error, error) {
	if parent == nil {
		return nil, nil, nil, failure("cp_path", "the copy parent is missing")
	}
	// O_PATH preserves ordinary write/search-only directory access: capturing
	// a capability must not add an unrelated directory-read requirement.
	directory, err := parent.OpenFile(".", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, nil, err
	}
	dirRaw, err := directory.SyscallConn()
	if err != nil {
		return nil, nil, nil, errors.Join(err, directory.Close())
	}
	fd := -1
	var openErr error
	err = dirRaw.Control(func(parentFD uintptr) {
		// O_EXCL here would make the anonymous inode impossible to publish.
		fd, openErr = unix.Openat(int(parentFD), ".", unix.O_TMPFILE|unix.O_RDWR|unix.O_CLOEXEC, 0600)
	})
	if err = errors.Join(err, openErr); err != nil {
		var fileCloseErr error
		if fd >= 0 {
			fileCloseErr = unix.Close(fd)
		}
		closeErr := errors.Join(fileCloseErr, directory.Close())
		if errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EISDIR) {
			return nil, nil, nil, &stageCommitError{errors.Join(failure("cp_path", "cannot prepare this copy destination"), err, closeErr)}
		}
		return nil, nil, nil, errors.Join(err, closeErr)
	}
	file := os.NewFile(uintptr(fd), "copy-stage")
	var original unix.Stat_t
	if err := unix.Fstat(fd, &original); err != nil || original.Mode&unix.S_IFMT != unix.S_IFREG || original.Nlink != 0 || original.Uid != uint32(os.Geteuid()) || original.Mode&0077 != 0 || original.Mode&07000 != 0 {
		return nil, nil, nil, errors.Join(failure("cp_path", "the copy stage has an unexpected owner or type"), err, file.Close(), directory.Close())
	}
	var mu sync.Mutex
	closed, attempted := false, false
	var closeErr error
	cleanup := func() error {
		mu.Lock()
		defer mu.Unlock()
		if !closed {
			closed = true
			closeErr = errors.Join(file.Close(), directory.Close())
		}
		return closeErr
	}
	publish := func(leaf string, replace bool) error {
		mu.Lock()
		defer mu.Unlock()
		if closed || attempted {
			return failure("cp_path", "the copy stage is closed or already published")
		}
		if err := Name(leaf); err != nil {
			return err
		}
		attempted = true
		raw, err := file.SyscallConn()
		if err != nil {
			return err
		}
		var result error
		// Control holds the actual file reference across all syscalls. Closing
		// the returned *os.File cannot turn its number into another source.
		controlErr := raw.Control(func(sourceFD uintptr) {
			var current unix.Stat_t
			if err := unix.Fstat(int(sourceFD), &current); err != nil {
				result = err
				return
			}
			if current.Dev != original.Dev || current.Ino != original.Ino || current.Mode&unix.S_IFMT != unix.S_IFREG || current.Nlink != 0 || current.Uid != original.Uid || current.Uid != uint32(os.Geteuid()) || current.Mode&07777 != 0644 {
				result = failure("cp_changed", "the checked copy stage changed before publication")
				return
			}
			parentErr := dirRaw.Control(func(parentFD uintptr) { result = linkStage(int(sourceFD), int(parentFD), leaf) })
			result = errors.Join(result, parentErr)
		})
		result = errors.Join(result, controlErr)
		if errors.Is(result, os.ErrExist) {
			refusal := errors.Join(failure("cp_exists", "a copy destination appeared before publication"), result)
			if replace {
				return &stageCommitError{refusal}
			}
			return refusal
		}
		return result
	}
	return file, publish, cleanup, nil
}

func linkStage(sourceFD, parentFD int, leaf string) error {
	err := unix.Linkat(sourceFD, "", parentFD, leaf, unix.AT_EMPTY_PATH)
	if err == nil || errors.Is(err, unix.EEXIST) {
		return err
	}
	// AT_EMPTY_PATH requires CAP_DAC_READ_SEARCH. The documented procfs
	// route links the SAME live descriptor without giving the helper a cap.
	if !errors.Is(err, unix.ENOENT) && !errors.Is(err, unix.EPERM) && !errors.Is(err, unix.EACCES) && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOSYS) {
		return err
	}
	var after unix.Stat_t
	if statErr := unix.Fstat(sourceFD, &after); statErr != nil {
		return errors.Join(err, statErr)
	}
	if after.Nlink != 0 {
		return err
	} // ambiguous publication: never unlink or retry a public name
	procFD, procErr := unix.Open("/proc/self/fd", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if procErr != nil {
		return errors.Join(err, procErr)
	}
	var filesystem unix.Statfs_t
	if fsErr := unix.Fstatfs(procFD, &filesystem); fsErr != nil || filesystem.Type != unix.PROC_SUPER_MAGIC {
		return errors.Join(err, failure("cp_path", "the live copy descriptor cannot be resolved safely"), fsErr, unix.Close(procFD))
	}
	linkErr := unix.Linkat(procFD, strconv.Itoa(sourceFD), parentFD, leaf, unix.AT_SYMLINK_FOLLOW)
	closeErr := unix.Close(procFD)
	if linkErr == nil {
		return closeErr
	}
	return errors.Join(err, linkErr, closeErr)
}
