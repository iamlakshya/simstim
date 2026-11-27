//go:build linux

package supply

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"golang.org/x/sys/unix"
)

// ErrRuntimeModeBusy is transient. Storage callers must release BOTH their
// store transaction and placement gate before retrying this native fence.
var ErrRuntimeModeBusy = errors.New("runtime artifact has a pending final exec")

// RuntimeModeGuard is an actual exclusive native OFD lease, never a data
// claim. It is short-lived for readers and retained through final exec for a
// direct VMM. Its original RO lock OFD must not be replaced by projection.
type RuntimeModeGuard struct {
	closeMu                          sync.Mutex
	metadataAttempted, lockAttempted bool
	metadataClose, lockClose         FileCloseFactsV1
	artifacts, modes                 *os.Root
	item                             Item
	metadata                         *os.File
	lock                             *os.File
	intent                           execModeIntent
	closed                           bool
}

// TryRuntimeModeGuard joins an EXISTING store-issued mode fence. A missing
// fence leaves ordinary immutable store validation unchanged. It never waits
// while the caller holds a store transaction, and never creates a namespace.
// Both roots are actual anchored capabilities from that transaction.
func TryRuntimeModeGuard(ctx context.Context, artifacts, modes *os.Root, item Item) (*RuntimeModeGuard, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if modes == nil {
		return nil, nil
	}
	lock, err := cachefs.OpenFile(modes, modeLeaseName(item), os.O_RDONLY, 0o600)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	held, err := cachefs.TryLock(lock, cachefs.Exclusive)
	if err != nil || !held {
		lock.Close()
		if err != nil {
			return nil, err
		}
		return nil, ErrRuntimeModeBusy
	}
	guard := &RuntimeModeGuard{artifacts: artifacts, modes: modes, item: item, lock: lock}
	if err := guard.loadOriginalIntent(); err != nil {
		guard.Close()
		return nil, err
	}
	return guard, nil
}

func (guard *RuntimeModeGuard) loadOriginalIntent() error {
	value, err := readExecModeIntent(guard.lock)
	if err != nil {
		return err
	}
	metadata, err := openModeArtifact(guard.artifacts, guard.item)
	if err != nil {
		return err
	}
	guard.metadata = metadata
	if err := value.checkNative(guard.item, guard.artifacts, guard.modes, metadata, guard.lock); err != nil {
		return err
	}
	guard.intent = value
	return nil
}

// Recover is permitted only by this ORIGINAL native exclusive fence plus
// exact protected producer intent and artifact identity. A JSON record, PID,
// timestamp or EOF supplied by a caller never substitutes for exclusivity.
// Every legitimate pre-final-exec participant retains the same lock OFD.
func (guard *RuntimeModeGuard) Recover(ctx context.Context) error {
	if guard == nil {
		return nil
	}
	if guard.closed || guard.lock == nil || guard.metadata == nil {
		return fmt.Errorf("runtime mode guard is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := guard.intent.checkNative(guard.item, guard.artifacts, guard.modes, guard.metadata, guard.lock); err != nil {
		return err
	}
	info, err := modeArtifactInfo(guard.artifacts, guard.item, guard.metadata)
	if err != nil {
		return err
	}
	if info.Mode() == 0o111 {
		if !guard.intent.Armed {
			return fmt.Errorf("execute-only runtime has no original armed native intent")
		}
		writable, err := modeMountWritable(guard.metadata)
		if err != nil {
			return err
		}
		if !writable {
			return fmt.Errorf("execute-only runtime cannot be repaired on a read-only artifact mount")
		}
		if err := chmodModeArtifact(guard.metadata, 0o555); err != nil {
			return err
		}
	}
	file, err := openModeReadable(guard.artifacts, guard.item, guard.metadata)
	if err != nil {
		return err
	}
	defer file.Close()
	if info.Mode() == 0o555 {
		// Inventory stays metadata-only. The immutable fs-verity measurement
		// binds the actual inode; data readers apply their existing SHA loop.
		got, err := measureFSVerity(file)
		if err != nil {
			return err
		}
		if got != guard.intent.FSVerity {
			return &MismatchError{Name: guard.item.Name, What: "fs-verity", Want: guard.intent.FSVerity, Got: got}
		}
		return ctx.Err()
	}
	if err := guard.checkImmutable(ctx, file); err != nil {
		// Never convert an identity/digest failure into a sealed fallback.
		// Fresh exclusivity already excludes future execs, but keep the
		// original armed state and mode on an untrusted recovery candidate.
		if info.Mode() == 0o111 {
			return errors.Join(err, chmodModeArtifact(guard.metadata, 0o111))
		}
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return guard.intent.checkNative(guard.item, guard.artifacts, guard.modes, guard.metadata, guard.lock)
}

func (guard *RuntimeModeGuard) checkImmutable(ctx context.Context, file *os.File) error {
	got, err := measureFSVerity(file)
	if err != nil {
		return fmt.Errorf("measure original mode artifact: %w", err)
	}
	if got != guard.intent.FSVerity {
		return &MismatchError{Name: guard.item.Name, What: "fs-verity", Want: guard.intent.FSVerity, Got: got}
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if _, err := file.Seek(0, 0); err != nil {
		return err
	}
	if err := checkReader(guard.item, guard.item.Name, info.Size(), contextualReadSeeker{ctx: ctx, file: file}); err != nil {
		return err
	}
	return ctx.Err()
}

// Close releases a reader guard. Direct-exec owners use the stricter phase
// owner instead, which cannot drop this fence with future execs outstanding.
func (guard *RuntimeModeGuard) Close() error {
	if guard == nil {
		return nil
	}
	guard.closeMu.Lock()
	defer guard.closeMu.Unlock()
	if guard.closed {
		return nil
	}
	var result error
	if guard.metadata != nil && !guard.metadataAttempted {
		guard.metadataAttempted = true
		guard.metadataClose = CloseFileOnceV1(guard.metadata)
		result = errors.Join(result, guard.metadataClose.Diagnostic)
	}
	if guard.lock != nil && !guard.lockAttempted {
		guard.lockAttempted = true
		guard.lockClose = CloseFileOnceV1(guard.lock)
		result = errors.Join(result, guard.lockClose.Diagnostic)
	}
	result = errors.Join(result, guard.metadataClose.Unknown, guard.lockClose.Unknown)
	guard.closed = (guard.metadata == nil || guard.metadataClose.Retired) && (guard.lock == nil || guard.lockClose.Retired)
	return result
}

// Provision only the EXISTING per-artifact registry from an actual verified
// installation, before cache metadata may become read-only. It issues no Cmd,
// map, role or execution authority. Caller owns the returned guard on errors.
func EnsureRuntimeModeIntentV1(ctx context.Context, artifacts, modes *os.Root, item Item) (*RuntimeModeGuard, error) {
	guard, err := createRuntimeModeGuard(ctx, artifacts, modes, item)
	if err != nil {
		return guard, err
	}
	if err := guard.Recover(ctx); err != nil {
		return guard, err
	}
	return guard, nil
}

func createRuntimeModeGuard(ctx context.Context, artifacts, modes *os.Root, item Item) (*RuntimeModeGuard, error) {
	guard, err := TryRuntimeModeGuard(ctx, artifacts, modes, item)
	if err != nil || guard != nil {
		return guard, err
	}
	if modes == nil {
		return nil, fmt.Errorf("direct runtime requires its owned mode directory")
	}
	metadata, err := openModeArtifact(artifacts, item)
	if err != nil {
		return nil, err
	}
	defer metadata.Close()
	file, err := openModeReadable(artifacts, item, metadata)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if err := CheckOpenContext(ctx, item, file); err != nil {
		return nil, err
	}
	got, err := measureFSVerity(file)
	if err != nil {
		return nil, err
	}
	if err := compareVerity(item, got); err != nil {
		return nil, err
	}
	record, err := cachefs.OpenFile(modes, modeLeaseName(item), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return TryRuntimeModeGuard(ctx, artifacts, modes, item)
	}
	if err != nil {
		return nil, err
	}
	defer record.Close()
	published := false
	defer func() {
		if published {
			return
		}
		// Remove only THIS exclusively created, never-armed incomplete record.
		// Existing unknown/foreign intent and every other inode are retained.
		own, err := record.Stat()
		current, cerr := modes.Lstat(modeLeaseName(item))
		if err == nil && cerr == nil && os.SameFile(own, current) {
			if _, err := cachefs.ValidateFile(modes, modeLeaseName(item), record, 0o600); err == nil {
				_ = modes.Remove(modeLeaseName(item))
			}
		}
	}()
	value, err := newExecModeIntent(item, artifacts, modes, file, record)
	if err != nil {
		return nil, err
	}
	if err := writeExecModeIntent(ctx, value, modes, item, record); err != nil {
		return nil, err
	}
	directory, err := modes.Open(".")
	if err != nil {
		return nil, err
	}
	err = directory.Sync()
	closeErr := directory.Close()
	if err != nil || closeErr != nil {
		return nil, errors.Join(err, closeErr)
	}
	published = true
	return TryRuntimeModeGuard(ctx, artifacts, modes, item)
}

// Mark the actual lock FD CLOEXEC only in the final external-exec stage.
// This helper is private to phase ownership; ordinary projection must leave
// the original OFD intact. It is not an executable or writable file grant.
func execModeCloseOnFinalExec(file *os.File) error {
	_, err := unix.FcntlInt(file.Fd(), unix.F_SETFD, unix.FD_CLOEXEC)
	return err
}
