//go:build linux

package supply

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

const runtimeSeals = unix.F_SEAL_WRITE | unix.F_SEAL_GROW | unix.F_SEAL_SHRINK | unix.F_SEAL_SEAL

// SealRuntime copies one manifest-authorised VMM or kernel into a fresh sealed
// descriptor. It retains the public artefact's bytes and checks them through
// the canonical reader. VMM copies are execute-only for the NoDump contract.
func SealRuntime(ctx context.Context, item Item, source *os.File) (sealed *os.File, returned error) {
	if err := validateSealedRuntimeItem(item); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := CheckOpen(item, source); err != nil {
		return nil, err
	}
	if _, err := source.Seek(0, 0); err != nil {
		return nil, fmt.Errorf("rewind source runtime: %w", err)
	}
	flags := unix.MFD_CLOEXEC | unix.MFD_ALLOW_SEALING
	if item.Kind == KindVMM {
		flags |= unix.MFD_EXEC
	} else {
		flags |= unix.MFD_NOEXEC_SEAL
	}
	fd, err := unix.MemfdCreate("runtime-"+item.Kind, flags)
	if err != nil {
		return nil, fmt.Errorf("create runtime copy: %w", err)
	}
	file := os.NewFile(uintptr(fd), "sealed-runtime")
	defer func() {
		if returned != nil {
			file.Close()
		}
	}()
	if _, err := io.Copy(file, io.LimitReader(&runtimeContextReader{ctx: ctx, reader: source}, item.Size+1)); err != nil {
		return nil, fmt.Errorf("copy runtime bytes: %w", err)
	}
	mode := os.FileMode(0o444)
	seals := runtimeSeals
	if item.Kind == KindVMM {
		mode = 0o111
		seals |= unix.F_SEAL_EXEC
	}
	if err := file.Chmod(mode); err != nil {
		return nil, fmt.Errorf("set runtime copy mode: %w", err)
	}
	if _, err := unix.FcntlInt(file.Fd(), unix.F_ADD_SEALS, seals); err != nil {
		return nil, fmt.Errorf("seal runtime bytes: %w", err)
	}
	if err := CheckSealedRuntime(item, file); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := file.Seek(0, 0); err != nil {
		return nil, fmt.Errorf("rewind sealed runtime: %w", err)
	}
	return file, nil
}

// CheckSealedRuntime permits an unlinked descriptor only after proving its
// complete seals, exact mode and canonical manifest size/SHA-256. CheckOpen
// retains its regular linked-file rules unchanged.
func CheckSealedRuntime(item Item, file *os.File) error {
	if err := validateSealedRuntimeItem(item); err != nil {
		return err
	}
	if file == nil {
		return fmt.Errorf("sealed runtime descriptor is missing")
	}
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat sealed runtime: %w", err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return fmt.Errorf("inspect sealed runtime inode: %w", err)
	}
	mode := os.FileMode(0o444)
	required := runtimeSeals
	if item.Kind == KindVMM {
		mode = 0o111
		required |= unix.F_SEAL_EXEC
	}
	seals, err := unix.FcntlInt(file.Fd(), unix.F_GET_SEALS, 0)
	if err != nil {
		return fmt.Errorf("read runtime seals: %w", err)
	}
	var filesystem unix.Statfs_t
	if err := unix.Fstatfs(int(file.Fd()), &filesystem); err != nil {
		return fmt.Errorf("inspect sealed runtime filesystem: %w", err)
	}
	if !info.Mode().IsRegular() || stat.Nlink != 0 || filesystem.Type != unix.TMPFS_MAGIC || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || info.Mode().Perm() != mode || seals&required != required {
		return fmt.Errorf("runtime copy is not an immutable descriptor with the required execution mode")
	}
	if _, err := file.Seek(0, 0); err != nil {
		return fmt.Errorf("seek sealed runtime: %w", err)
	}
	if err := checkReader(item, item.Name, info.Size(), file); err != nil {
		return err
	}
	after, err := file.Stat()
	if err != nil {
		return fmt.Errorf("recheck sealed runtime identity: %w", err)
	}
	afterSeals, err := unix.FcntlInt(file.Fd(), unix.F_GET_SEALS, 0)
	if err != nil {
		return fmt.Errorf("recheck runtime seals: %w", err)
	}
	if !os.SameFile(info, after) || after.Size() != info.Size() || after.Mode().Perm() != mode || after.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || afterSeals&required != required {
		return fmt.Errorf("sealed runtime descriptor identity or execution mode changed")
	}
	if _, err := file.Seek(0, 0); err != nil {
		return fmt.Errorf("rewind checked sealed runtime: %w", err)
	}
	return nil
}

func validateSealedRuntimeItem(item Item) error {
	if item.Arch != runtime.GOARCH {
		return fmt.Errorf("the sealed runtime architecture differs from this host")
	}
	if item.Kind != KindVMM && item.Kind != KindKernel {
		return fmt.Errorf("only a VMM or guest kernel uses a sealed runtime copy")
	}
	if err := item.validate(); err != nil {
		return err
	}
	if item.Size == math.MaxInt64 {
		return fmt.Errorf("runtime size cannot include its excess-byte check")
	}
	return nil
}

type runtimeContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *runtimeContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
