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

// SealRuntime copies one manifest-authorised runtime into a fresh sealed
// descriptor. It retains the public artefact's bytes and checks them through
// the canonical reader. VMM copies are execute-only for the NoDump contract.
// gVisor executables keep their readable mode. Their patched entry points and
// native caller must establish NoDump before configuration or secret input.
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
	fd, err := createRuntimeMemfd(item.Kind, unix.MemfdCreate)
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
	if err := file.Chmod(sealedRuntimeMode(item.Kind)); err != nil {
		return nil, fmt.Errorf("set runtime copy mode: %w", err)
	}
	if err := sealRuntimeMemfd(file.Fd(), item.Kind, unix.FcntlInt); err != nil {
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
// immutable byte seals, exact mode and canonical manifest size/SHA-256.
// Execution separately requires read-only mount projection. The execution
// seal, where available, protects execute bits but does not freeze read bits.
// CheckOpen retains its regular linked-file rules unchanged.
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
	mode := sealedRuntimeMode(item.Kind)
	required := runtimeSeals
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
	if !runtimeExecutableKind(item.Kind) && item.Kind != KindKernel {
		return fmt.Errorf("only a VMM, gVisor executable or guest kernel uses a sealed runtime copy")
	}
	if err := item.validate(); err != nil {
		return err
	}
	if item.Size == math.MaxInt64 {
		return fmt.Errorf("runtime size cannot include its excess-byte check")
	}
	return nil
}

func sealedRuntimeMode(kind string) os.FileMode {
	switch kind {
	case KindVMM:
		return 0o111
	case KindGvisor:
		return 0o555
	default:
		return 0o444
	}
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
