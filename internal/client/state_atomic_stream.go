package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// stateStreamWriter retains ignored I/O failures so a callback cannot publish
// a partial replacement by discarding a contextual or short-write error.
type stateStreamWriter struct {
	mu      sync.Mutex
	revoked bool
	ctx     context.Context
	file    *os.File
	err     error
}

func (w *stateStreamWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.revoked {
		return 0, os.ErrClosed
	}
	if w.err != nil {
		return 0, w.err
	}
	if err := w.ctx.Err(); err != nil {
		w.err = err
		return 0, err
	}
	n, err := w.file.Write(b)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	if stopped := w.ctx.Err(); stopped != nil {
		err = stopped
	}
	if err != nil {
		w.err = err
	}
	return n, err
}

func (tx *stateFileTxn) replace(replace stateFileReplace) (err error) {
	if tx.root == nil || tx.lock == nil {
		return badGrantStateError(tx.name)
	}
	if err := tx.check(); err != nil {
		return err
	}
	beforeGrantsStateWrite()
	if err := tx.check(); err != nil {
		return err
	}
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	if err := tx.ctx.Err(); err != nil {
		return err
	}
	name := "." + tx.name + ".tmp-" + hex.EncodeToString(random[:])
	f, err := tx.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|grantsStateNoFollow, 0600)
	if err != nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			// A swapped name is never cleanup authority for a foreign inode.
			if info, statErr := f.Stat(); statErr == nil && stateCreatedTempSafe(f, info) && stateRegularIdentityMatches(tx.root, name, f) {
				if cleanup := tx.root.Remove(name); cleanup != nil {
					err = errors.Join(err, wrapGrantStateError(name, cleanup))
				}
			}
			_ = f.Close()
		}
	}()
	before, err := f.Stat()
	if err != nil || !before.Mode().IsRegular() {
		return badGrantStateError(name)
	}

	if err := tx.ctx.Err(); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		if err := f.Chmod(0600); err != nil {
			return err
		}
	}
	if err := tx.check(); err != nil {
		return err
	}
	if err := stateRegularMatchesContext(tx.ctx, tx.root, name, f); err != nil {
		return err
	}
	if err := stateTemporaryOwned(tx.ctx, f); err != nil {
		return err
	}
	writer := &stateStreamWriter{ctx: tx.ctx, file: f}
	defer writer.finish()
	callbackErr := replace(tx.ctx, writer)
	writeErr := writer.finish()
	if callbackErr != nil {
		return &stateCallbackError{err: callbackErr}
	}
	if writeErr != nil {
		return writeErr
	}
	if err := tx.check(); err != nil {
		return err
	}
	if err := stateRegularMatchesContext(tx.ctx, tx.root, name, f); err != nil {
		return err
	}
	if err := stateTemporaryOwned(tx.ctx, f); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := tx.check(); err != nil {
		return err
	}
	if err := stateRegularMatchesContext(tx.ctx, tx.root, name, f); err != nil {
		return err
	}
	if err := stateTemporaryOwned(tx.ctx, f); err != nil {
		return err
	}
	if err := tx.ctx.Err(); err != nil {
		return err
	}
	if err := tx.root.Rename(name, tx.name); err != nil {
		return fmt.Errorf("save %s: %w", tx.name, err)
	}
	published, tx.output = true, f
	afterStateFilePublish()
	// A committed namespace operation finishes its own finite flush even
	// when the caller cancels. The committed rename is never rolled back.
	return tx.finishCommittedMutation()
}

func (tx *stateFileTxn) finishCommittedMutation() error {
	finish := context.WithoutCancel(tx.ctx)
	var flush error
	if runtime.GOOS == "windows" && tx.output != nil {
		flush = tx.output.Sync()
	} else {
		flush = syncStateDirectory(finish, tx.root)
	}
	snapshot := tx.checkSnapshot(finish)
	if err := tx.ctx.Err(); err != nil {
		return err
	}
	if err := stateContextAuthority(tx.ctx); err != nil {
		return err
	}
	if tx.authority != nil {
		if err := tx.authority(); err != nil {
			return err
		}
	}
	return errors.Join(flush, snapshot)
}

func stateTemporaryOwned(ctx context.Context, f *os.File) error {
	_, err := stateSafeDescriptor(ctx, f)
	return err
}

func syncStateDirectory(ctx context.Context, r *os.Root) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Windows publication flushes its writable file handle. There is no
	// read-only directory FlushFileBuffers promise for cleanup here.
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := r.Open(".")
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		_ = dir.Close()
		return err
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	if stopped := ctx.Err(); stopped != nil {
		return stopped
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func (w *stateStreamWriter) finish() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.revoked = true
	w.file = nil
	return w.err
}

type stateCallbackError struct{ err error }

func (e *stateCallbackError) Error() string { return e.err.Error() }
func (e *stateCallbackError) Unwrap() error { return e.err }

func stateCreatedTempSafe(file *os.File, info os.FileInfo) bool {
	_, err := cachefs.ValidateDescriptor(file, info.Mode().Perm())
	return err == nil && info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0
}
