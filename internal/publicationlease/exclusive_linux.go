//go:build linux

package publicationlease

import (
	"context"
	"errors"
	"os"
	"sync"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// ExclusiveV1 retains the actual fresh exclusive OFD. A successful probe
// proves only that original inherited helper holders have all dropped it.
// Canonical ledger removal additionally requires its original owner/handler
// settlement. This object is never inherited by helpers.
type ExclusiveV1 struct {
	mu                      sync.Mutex
	root                    *os.Root
	file                    *os.File
	record                  RecordV1
	encoded                 string
	removed, synced, closed bool
}

// TryExclusiveV1 opens a DIFFERENT parent-only OFD on the exact native
// sidecar recorded by the original operation, then tries EX without waiting.
// Unknown, missing, replaced and malformed entries fail; none becomes an
// unfenced operation or a successful parent-PID cleanup.
func TryExclusiveV1(ctx context.Context, root *os.Root, domain cachefs.Domain, encoded string) (_ *ExclusiveV1, returned error) {
	if ctx == nil || root == nil {
		return nil, failureV1("the original publication probe has no actual control root")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	record, err := DecodeRecordV1(encoded)
	if err != nil {
		return nil, err
	}
	if record.Domain != domain {
		return nil, failureV1("the publication fence belongs to another host domain")
	}
	if err := record.Root.CheckRoot(root); err != nil {
		return nil, err
	}
	owned, err := root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	proof := &ExclusiveV1{root: owned, record: record, encoded: encoded}
	defer func() {
		if returned != nil {
			_ = proof.CloseV1()
		}
	}()
	// RW is parent-only and supports native EX on filesystems that require
	// it. Children inherit only the original independently opened RO SH OFD.
	proof.file, err = cachefs.OpenFile(owned, nameV1(record.Token), os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := proof.checkV1(); err != nil {
		return nil, err
	}
	locked, err := cachefs.TryLock(proof.file, cachefs.Exclusive)
	if err != nil {
		return nil, err
	}
	if !locked {
		return nil, ErrBusy
	}
	if err := proof.checkV1(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return proof, nil
}
func (proof *ExclusiveV1) checkV1() error {
	if proof == nil || proof.closed || proof.root == nil || proof.file == nil {
		return failureV1("the original exclusive publication proof is missing")
	}
	if err := proof.record.Root.CheckRoot(proof.root); err != nil {
		return err
	}
	if err := proof.record.Self.CheckCreatedFile(proof.root, nameV1(proof.record.Token), proof.file, 0o600); err != nil {
		return err
	}
	_, encoded, err := readRecordV1(proof.file)
	if err != nil {
		return err
	}
	if encoded != proof.encoded {
		return failureV1("the original publication fence record changed")
	}
	return nil
}

// CheckV1 repeats native role/identity/record checks on the held fresh EX.
// It does not reacquire or upgrade the original inherited shared OFD.
func (proof *ExclusiveV1) CheckV1() error {
	if proof == nil {
		return failureV1("the exclusive publication proof is missing")
	}
	proof.mu.Lock()
	defer proof.mu.Unlock()
	return proof.checkV1()
}

// RemoveV1 removes only the proven sidecar. The operation owner must order
// its canonical Active/control write and native deletion so a failed write
// retains an explicit retryable receipt. ENOENT is never adopted as success.
func (proof *ExclusiveV1) RemoveV1() error {
	if proof == nil {
		return failureV1("the exclusive publication proof is missing")
	}
	proof.mu.Lock()
	defer proof.mu.Unlock()
	if proof.synced {
		return nil
	}
	if !proof.removed {
		if err := proof.checkV1(); err != nil {
			return err
		}
		if err := proof.root.Remove(nameV1(proof.record.Token)); err != nil {
			return err
		}
		proof.removed = true // Actual unlink succeeded; never replace it with ENOENT.
	}
	directory, err := proof.root.Open(".")
	if err != nil {
		return err
	}
	err = errors.Join(cachefs.SyncDirectory(directory), directory.Close())
	if err == nil {
		proof.synced = true
	}
	return err
}

// CloseV1 drops this parent-only EX and root. It does not edit a ledger.
func (proof *ExclusiveV1) CloseV1() error {
	if proof == nil {
		return nil
	}
	proof.mu.Lock()
	defer proof.mu.Unlock()
	if proof.closed {
		return nil
	}
	proof.closed = true
	var result error
	if proof.file != nil {
		result = errors.Join(result, proof.file.Close())
		proof.file = nil
	}
	if proof.root != nil {
		result = errors.Join(result, proof.root.Close())
		proof.root = nil
	}
	return result
}

// RemovalDoneV1 observes this SAME object's actual successful unlink. It is
// not a saved-field constructor and grants no action on any other pathname.
func (proof *ExclusiveV1) RemovalDoneV1() bool {
	if proof == nil {
		return false
	}
	proof.mu.Lock()
	defer proof.mu.Unlock()
	return proof.removed
}
