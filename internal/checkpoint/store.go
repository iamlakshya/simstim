package checkpoint

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/proto"
)

const diskName = "disk.img"

// Store borrows one runtime factory's original disk and rooted descriptors.
// Close releases only the checkpoint child root opened by this store. The
// caller retains and closes its original disk, session root and disk root.
type Store struct {
	mu                           sync.Mutex
	session, diskRoot, entries   *os.Root
	sessionRef, diskRootRef      cachefs.Reference
	entriesRef, diskRef          cachefs.Reference
	disk                         *os.File
	check                        func(context.Context) error
	closed                       bool
	closeErr                     error
	clone                        func(*os.File, *os.File) error
	sequence                     uint64
	safetySource                 cachefs.Reference
	pendingRestore               *localPreparedRestoreV1
	ownedDisk                    *os.File
	createdKey                   string
	createdDisk, createdMetadata cachefs.Reference
}

// OpenOwned accepts only the actual runtime creator's retained session root,
// vm root and current disk descriptor. check must prove that SAME original
// native source and operation on every use. The roots and disk are borrowed.
// This constructor does not turn filenames, manifests or DTOs into authority.
func OpenOwned(ctx context.Context, sessionRoot, diskRoot *os.Root, originalDisk *os.File, check func(context.Context) error) (_ *Store, returned error) {
	if ctx == nil || check == nil || sessionRoot == nil || diskRoot == nil || originalDisk == nil {
		return nil, failure("checkpoint storage has no original native owner", nil)
	}
	if err := errors.Join(ctx.Err(), check(ctx)); err != nil {
		return nil, err
	}
	s := &Store{session: sessionRoot, diskRoot: diskRoot, disk: originalDisk, check: check, clone: cloneFile}
	var err error
	s.sessionRef, err = cachefs.CaptureRoot(sessionRoot)
	if err != nil {
		return nil, failure("cannot pin the original session root", err)
	}
	s.diskRootRef, err = cachefs.CaptureRoot(diskRoot)
	if err != nil {
		return nil, failure("cannot pin the original disk root", err)
	}
	s.diskRef, err = cachefs.CaptureFile(diskRoot, diskName, originalDisk, 0o600)
	if err != nil {
		return nil, failure("cannot pin the original session disk", err)
	}
	s.entries, err = cachefs.OpenPrivateChild(sessionRoot, "checkpoints", true)
	if err != nil {
		return nil, failure("cannot open the private checkpoint folder", err)
	}
	complete := false
	defer func() {
		if !complete {
			returned = errors.Join(returned, s.entries.Close())
		}
	}()
	s.entriesRef, err = cachefs.CaptureRoot(s.entries)
	if err != nil {
		return nil, failure("cannot pin the private checkpoint folder", err)
	}
	if err := s.checkOwned(ctx, check); err != nil {
		return nil, err
	}
	complete = true
	return s, nil
}

func (s *Store) checkOwned(ctx context.Context, owner func(context.Context) error) error {
	if s == nil || s.closed || ctx == nil || owner == nil {
		return failure("the checkpoint store has no retained original owner", nil)
	}
	if err := errors.Join(ctx.Err(), owner(ctx)); err != nil {
		return err
	}
	if err := errors.Join(s.sessionRef.CheckRoot(s.session), s.diskRootRef.CheckRoot(s.diskRoot), s.entriesRef.CheckRoot(s.entries), s.diskRef.CheckCreatedFile(s.diskRoot, diskName, s.disk, 0o600)); err != nil {
		return failure("the original checkpoint roots or disk changed", err)
	}
	if err := checkNamedRoot(s.session, "vm", s.diskRoot); err != nil {
		return failure("the original disk folder changed identity", err)
	}
	return checkNamedRoot(s.session, "checkpoints", s.entries)
}

func checkNamedRoot(parent *os.Root, name string, child *os.Root) error {
	// A root alone is not a role binding. Check the SAME named directory.
	named, err := parent.Lstat(name)
	if err != nil {
		return failure("the original checkpoint folder changed", err)
	}
	opened, err := child.Open(".")
	if err != nil {
		return failure("cannot inspect the original rooted directory", err)
	}
	info, statErr := opened.Stat()
	closeErr := opened.Close()
	if statErr != nil || closeErr != nil || !named.IsDir() || !os.SameFile(named, info) {
		return failure("the original checkpoint folder changed identity", errors.Join(statErr, closeErr))
	}
	return nil
}

// Check repeats the original producer's current ownership proof.
func (s *Store) Check(ctx context.Context) error {
	if s == nil {
		return failure("the checkpoint store is absent", nil)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checkOwned(ctx, s.check)
}

// Close joins this store's current operation and closes its private child root.
// It does not close the borrowed original disk or the caller's native roots.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.closeErr
	}
	if s.pendingRestore != nil {
		return failure("the original rewind transaction must settle before its store closes", nil)
	}
	s.closed = true
	var ownedErr error
	if s.ownedDisk != nil {
		ownedErr = s.ownedDisk.Close()
	}
	if err := errors.Join(s.entries.Close(), ownedErr); err != nil {
		s.closeErr = failure("cannot close the original checkpoint folder", err)
		return s.closeErr
	}
	return nil
}

func (s *Store) noPreparedRestoreV1() error {
	if s.pendingRestore != nil {
		return failure("the original disk has a prepared rewind transaction", nil)
	}
	return nil
}

func failure(message string, cause error) error {
	if cause != nil {
		message += ": " + cause.Error()
	}
	return &operationError{wire: &proto.Error{Code: "checkpoint", Message: message, Hint: "Check the session's original runtime and free disk space, then try again."}, cause: cause}
}

func off(cause error) error {
	return &operationError{wire: &proto.Error{Code: "rewind_off", Message: "checkpoints are off: this session disk's filesystem cannot clone files: " + cause.Error(), Hint: "Use a microvm on a filesystem whose FICLONE probe succeeds."}, cause: cause}
}

type operationError struct {
	wire  *proto.Error
	cause error
}

func (e *operationError) Error() string { return e.wire.Error() }

func (e *operationError) Unwrap() []error {
	if e.cause == nil {
		return []error{e.wire}
	}
	return []error{e.wire, e.cause}
}

func cloneFailure(message string, cause error) error {
	if cloneUnsupported(cause) {
		return off(cause)
	}
	return failure(message, cause)
}

type ownedFile struct {
	root *os.Root
	file *os.File
	name string
	ref  cachefs.Reference
}

func createPart(root *os.Root) (*ownedFile, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, failure("cannot choose a private checkpoint part name", err)
	}
	name := ".checkpoint-" + hex.EncodeToString(token[:]) + ".part"
	file, err := cachefs.OpenFile(root, name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	ref, err := cachefs.CaptureFile(root, name, file, 0o600)
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return &ownedFile{root: root, file: file, name: name, ref: ref}, nil
}

func (f *ownedFile) remove() error {
	if f == nil || f.file == nil {
		return nil
	}
	check := f.ref.CheckCreatedFile(f.root, f.name, f.file, 0o600)
	if check != nil {
		return errors.Join(check, f.file.Close()) // Preserve a substituted leaf.
	}
	return errors.Join(f.root.Remove(f.name), f.file.Close())
}

func (f *ownedFile) publish(name string) error {
	if err := f.ref.CheckCreatedFile(f.root, f.name, f.file, 0o600); err != nil {
		return err
	}
	if err := cachefs.RenameNoReplace(f.root, f.name, name); err != nil {
		return fmt.Errorf("publish checkpoint file: %w", err)
	}
	f.name = name
	return f.ref.CheckCreatedFile(f.root, name, f.file, 0o600)
}

func syncRoot(root *os.Root) error {
	file, err := root.Open(".")
	if err != nil {
		return failure("cannot open the private checkpoint folder for sync", err)
	}
	return errors.Join(file.Sync(), file.Close())
}
