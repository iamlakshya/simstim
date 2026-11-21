//go:build linux

package supply

import (
	"context"
	"fmt"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

// VerifiedRuntime owns one immutable descriptor. Only PrepareRuntime can
// construct it; it grants no host operation or boot-selection authority.
type VerifiedRuntime struct {
	mu       sync.Mutex
	file     *os.File
	item     Item
	identity os.FileInfo
	verity   bool
	measure  func(*os.File) (string, error)
	mode     *execModeCandidate
	closeErr error
}

// PrepareRuntime borrows the original owned store descriptor. A kernel with
// matching fs-verity uses that same inode; otherwise it uses a sealed copy.
// A VMM retains the execute-only sealed projection required by NoDump, even
// after successful source measurement. Direct readable VMM execution remains
// an unresolved specification boundary, not a claimed completed route.
func PrepareRuntime(ctx context.Context, item Item, source *os.File) (*VerifiedRuntime, error) {
	return prepareRuntime(ctx, item, source, nativeVerityCalls())
}
func prepareRuntime(ctx context.Context, item Item, source *os.File, calls verityCalls) (_ *VerifiedRuntime, returned error) {
	if err := validateSealedRuntimeItem(item); err != nil {
		return nil, err
	}
	if !fsVerityKind(item.Kind) {
		return nil, fmt.Errorf("verified runtime selection needs a VMM or kernel")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := runtimeStoreDescriptor(item, source, false); err != nil {
		return nil, err
	}
	fd, err := unix.FcntlInt(source.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("retain runtime source: %w", err)
	}
	file := os.NewFile(uintptr(fd), "verified-runtime")
	defer func() {
		if returned != nil {
			file.Close()
		}
	}()
	before, err := runtimeStoreDescriptor(item, file, false)
	if err != nil {
		return nil, err
	}
	got, measurement := calls.measure(file)
	if measurement == nil {
		if err := compareVerity(item, got); err != nil {
			return nil, err
		}
		if err := CheckOpenContext(ctx, item, file); err != nil {
			return nil, err
		}
		after, err := runtimeStoreDescriptor(item, file, true)
		if err != nil {
			return nil, err
		}
		if !os.SameFile(before, after) {
			return nil, fmt.Errorf("measured runtime changed inode")
		}
		if item.Kind == KindKernel {
			if _, err := file.Seek(0, 0); err != nil {
				return nil, err
			}
			return &VerifiedRuntime{file: file, item: item, identity: before, verity: true, measure: calls.measure}, ctx.Err()
		}
	} else if !verityAbsent(measurement) {
		return nil, fmt.Errorf("measure runtime source: %w", measurement)
	}
	sealed, err := SealRuntime(ctx, item, file)
	if err != nil {
		return nil, err
	}
	file.Close()
	file = sealed
	return &VerifiedRuntime{file: sealed, item: item}, nil
}

// File returns a BORROWED descriptor. The boot owner retains this object
// through physical teardown and calls Close; it never closes File separately.
// Revalidation uses this descriptor, never a store path or a policy requery.
func (v *VerifiedRuntime) File() (*os.File, error) {
	if v == nil {
		return nil, fmt.Errorf("verified runtime is missing")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.file == nil {
		return nil, os.ErrClosed
	}
	if v.mode != nil {
		if err := v.mode.checkProgram(context.Background()); err != nil {
			return nil, err
		}
		return v.file, nil
	}
	if v.verity {
		info, err := runtimeStoreDescriptor(v.item, v.file, true)
		if err != nil {
			return nil, err
		}
		if !os.SameFile(v.identity, info) {
			return nil, fmt.Errorf("verified runtime changed identity")
		}
		got, err := v.measure(v.file)
		if err != nil {
			return nil, err
		}
		if err := compareVerity(v.item, got); err != nil {
			return nil, err
		}
	} else if err := CheckSealedRuntime(v.item, v.file); err != nil {
		return nil, err
	}
	return v.file, nil
}

// TakeFileForCloseV1 transfers cleanup ownership of the SAME actual file only
// after its existing native mode/root cleanup succeeds. It reopens nothing,
// issues no execution source, and preserves an earlier unknown Close failure.
func (v *VerifiedRuntime) TakeFileForCloseV1() (*os.File, error) {
	if v == nil {
		return nil, nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closeErr != nil {
		return nil, v.closeErr
	}
	if v.file == nil {
		return nil, nil
	}
	if v.mode != nil {
		if err := v.mode.close(); err != nil {
			return nil, err
		}
	}
	actual := v.file
	v.file = nil
	return actual, nil
}

// Close preserves the actual owner on an unclassified descriptor error. The
// VM-specific custody consumer uses TakeFileForCloseV1 before its one concrete
// guarded first close. No error payload or path reconstructs that ownership.
func (v *VerifiedRuntime) Close() error {
	if v == nil {
		return nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closeErr != nil {
		return v.closeErr
	}
	if v.file == nil {
		return nil
	}
	if v.mode != nil {
		if err := v.mode.close(); err != nil {
			return err
		}
	}
	err := v.file.Close()
	if err != nil {
		v.closeErr = err
		return err
	}
	v.file = nil
	return nil
}
