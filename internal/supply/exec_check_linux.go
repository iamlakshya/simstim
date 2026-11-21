//go:build linux

package supply

import (
	"context"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// CheckRuntimeDescriptor checks the final inherited boot capability, without
// opening a path or constructing an owner. Its caller retains the ORIGINAL
// boot/Need binding, descriptor lifetime, UID checks and read-only projection.
// A kernel may be a measured immutable inode or a checked sealed fallback.
// VMM execution remains strictly sealed/execute-only for the NoDump boundary.
func CheckRuntimeDescriptor(ctx context.Context, item Item, file *os.File) error {
	return checkRuntimeDescriptor(ctx, item, file, nativeVerityCalls())
}

func checkRuntimeDescriptor(ctx context.Context, item Item, file *os.File, calls verityCalls) error {
	if err := validateSealedRuntimeItem(item); err != nil {
		return err
	}
	if !fsVerityKind(item.Kind) {
		return fmt.Errorf("a final boot descriptor needs a VMM or kernel")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if item.Kind == KindVMM {
		if err := CheckSealedRuntime(item, file); err != nil {
			return err
		}
		return ctx.Err()
	}
	if file == nil {
		return fmt.Errorf("inherited kernel descriptor is missing")
	}
	got, err := calls.measure(file)
	if err != nil {
		if !verityAbsent(err) {
			return fmt.Errorf("measure inherited kernel descriptor: %w", err)
		}
		if err := CheckSealedRuntime(item, file); err != nil {
			return err
		}
		return ctx.Err()
	}
	if err := compareVerity(item, got); err != nil {
		return err
	}
	before, err := measuredKernelDescriptor(item, file)
	if err != nil {
		return err
	}
	if _, err := file.Seek(0, 0); err != nil {
		return err
	}
	if err := checkReader(item, item.Name, before.Size(), contextualReadSeeker{ctx: ctx, file: file}); err != nil {
		return err
	}
	if err := checkKernelEFIItemV1(ctx, item, file); err != nil {
		return err
	}
	after, err := measuredKernelDescriptor(item, file)
	if err != nil {
		return err
	}
	if !os.SameFile(before, after) {
		return fmt.Errorf("inherited measured kernel changed inode")
	}
	got, err = calls.measure(file)
	if err != nil {
		return fmt.Errorf("recheck inherited kernel measurement: %w", err)
	}
	if err := compareVerity(item, got); err != nil {
		return err
	}
	if _, err := file.Seek(0, 0); err != nil {
		return err
	}
	return ctx.Err()
}

func measuredKernelDescriptor(item Item, file *os.File) (os.FileInfo, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return nil, err
	}
	flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() != item.Size || info.Mode() != 0o444 || stat.Nlink > 1 || flags&unix.O_ACCMODE != unix.O_RDONLY {
		return nil, fmt.Errorf("inherited measured kernel lost its native read-only file invariants")
	}
	// Do not compare inode UID with the jailed euid: original login-user files
	// map to UID0, while the real subordinate helper may run as UID1. The
	// producer already established native store ownership before the mapping.
	return info, nil
}
