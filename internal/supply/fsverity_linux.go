//go:build linux

package supply

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Linux UAPI fsverity.h: _IOW('f',133,128), _IOWR('f',134,4).
const fsIOCEnableVerity, fsIOCMeasureVerity = 0x40806685, 0xc0046686

type fsVerityEnableArg struct {
	Version, Algorithm, BlockSize, SaltSize uint32
	SaltPtr                                 uint64
	SigSize, Reserved                       uint32
	SigPtr                                  uint64
	Reserved2                               [11]uint64
}
type fsVerityMeasurement struct {
	Algorithm, Size uint16
	Digest          [32]byte
}

type verityCalls struct {
	enable  func(*os.File) error
	measure func(*os.File) (string, error)
}

func nativeVerityCalls() verityCalls {
	return verityCalls{enable: enableFSVerity, measure: measureFSVerity}
}
func enableFSVerity(file *os.File) error {
	arg := fsVerityEnableArg{Version: 1, Algorithm: 1, BlockSize: fsVerityBlock}
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, file.Fd(), fsIOCEnableVerity, uintptr(unsafe.Pointer(&arg)))
	runtime.KeepAlive(file)
	if errno != 0 {
		return errno
	}
	return nil
}
func measureFSVerity(file *os.File) (string, error) {
	arg := fsVerityMeasurement{Size: 32}
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, file.Fd(), fsIOCMeasureVerity, uintptr(unsafe.Pointer(&arg)))
	runtime.KeepAlive(file)
	if errno != 0 {
		return "", errno
	}
	if arg.Algorithm != 1 || arg.Size != 32 {
		return "", fmt.Errorf("fs-verity measurement is not a SHA-256 digest")
	}
	return hex.EncodeToString(arg.Digest[:]), nil
}
func verityAbsent(err error) bool {
	return errors.Is(err, unix.ENODATA) || verityUnsupported(err)
}
func verityUnsupported(err error) bool {
	return errors.Is(err, unix.ENOTTY) || errors.Is(err, unix.EOPNOTSUPP)
}

// InstallRuntimeVerity enables fs-verity on the same caller-owned, read-only
// VMM/kernel inode after publication. The caller holds the store transaction,
// has closed every writable descriptor and retains native name/owner checks.
// Unsupported filesystems keep the ordinary immutable store mode and later
// use sealed copies. A measurement mismatch or other ioctl failure refuses.
func InstallRuntimeVerity(ctx context.Context, item Item, file *os.File) (bool, error) {
	return installRuntimeVerity(ctx, item, file, nativeVerityCalls())
}
func installRuntimeVerity(ctx context.Context, item Item, file *os.File, calls verityCalls) (enabled bool, returned error) {
	if !fsVerityKind(item.Kind) || item.FSVerity == "" {
		return false, ctx.Err()
	}
	if err := validateSealedRuntimeItem(item); err != nil {
		return false, err
	}
	before, err := runtimeStoreDescriptor(item, file, false)
	if err != nil {
		return false, err
	}
	if err := CheckOpenContext(ctx, item, file); err != nil {
		return false, err
	}
	got, err := calls.measure(file)
	if err == nil {
		if err := compareVerity(item, got); err != nil {
			return false, err
		}
		return true, ctx.Err()
	}
	if !verityAbsent(err) {
		return false, fmt.Errorf("measure installed runtime: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	mode := before.Mode().Perm()
	if err := file.Chmod(mode | 0o200); err != nil {
		return false, fmt.Errorf("allow runtime verity installation: %w", err)
	}
	defer func() {
		returned = errors.Join(returned, file.Chmod(mode))
		if err := returned; err == nil {
			_, returned = runtimeStoreDescriptor(item, file, false)
		}
	}()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	err = calls.enable(file)
	if err != nil && !errors.Is(err, unix.EEXIST) {
		if verityUnsupported(err) {
			return false, nil
		}
		return false, fmt.Errorf("enable runtime fs-verity: %w", err)
	}
	got, err = calls.measure(file)
	if err != nil {
		return false, fmt.Errorf("measure enabled runtime: %w", err)
	}
	if err := compareVerity(item, got); err != nil {
		return false, err
	}
	return true, ctx.Err()
}

func compareVerity(item Item, got string) error {
	if item.FSVerity == "" || got != item.FSVerity {
		return &MismatchError{Name: item.Name, What: "fs-verity", Want: item.FSVerity, Got: got}
	}
	return nil
}

func runtimeStoreDescriptor(item Item, file *os.File, unlinked bool) (os.FileInfo, error) {
	if file == nil {
		return nil, fmt.Errorf("runtime source descriptor is missing")
	}
	before, err := file.Stat()
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
	mode := os.FileMode(0o444)
	if item.Kind == KindVMM {
		mode = 0o555
	}
	if !before.Mode().IsRegular() || before.Size() != item.Size || before.Mode() != mode || stat.Uid != uint32(os.Geteuid()) || stat.Nlink > 1 || (!unlinked && stat.Nlink != 1) || flags&unix.O_ACCMODE != unix.O_RDONLY {
		return nil, fmt.Errorf("runtime source is not the owned read-only store inode of the expected size and mode")
	}
	return before, nil
}
