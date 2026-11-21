//go:build linux

package nitrodev

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// Device is the only native device supported.
const Device = "/dev/nitro_enclaves"

// Sysfs is the native Nitro pool and identity namespace.
const Sysfs = "/sys/class/misc/nitro_enclaves"

// RequiredFlags is PCIe, arbitrary virtio devices and floating vCPUs in the
// pinned v1.18.0 NE_GET_SUPPORTED_FLAGS contract. It changes no device state.
const RequiredFlags uint32 = 1<<6 | 1<<9 | 1<<11
const getSupportedFlags = 0x8004ae29

// ParentKernelHint names the supported parent kernel when the old Enclaves
// device exists but lacks the Sandboxes ioctl interface.
const ParentKernelHint = "Update the parent to Amazon Linux 2023 kernel6.18 version 6.18.51-120.162.amzn2023 or later, then probe again."

// Open returns this exact native character device after a read-only capability
// query. The caller must retain it through binding; its path is no authority.
func Open() (*os.File, error) { return OpenAt(Device, Sysfs) }

// OpenAt is also used by the canonical host probe with its fixed rooted source.
// It accepts no request-selected path from a guest, API or launcher request.
func OpenAt(path, sysfs string) (_ *os.File, returned error) {
	anchor, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("anchor native Nitro device: %w", err)
	}
	defer unix.Close(anchor)
	var original unix.Stat_t
	if err := unix.Fstat(anchor, &original); err != nil {
		return nil, fmt.Errorf("inspect anchored Nitro device: %w", err)
	}
	number, err := deviceNumber(sysfs)
	if err != nil {
		return nil, fmt.Errorf("read the Nitro device identity: %w", err)
	}
	if !Matches(original.Mode, uint64(original.Rdev), string(number)) {
		return nil, errors.New("the Nitro path is not its native misc character device")
	}
	fd, err := unix.Open(fmt.Sprintf("/proc/self/fd/%d", anchor), unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open anchored Nitro device: %w", err)
	}
	file := os.NewFile(uintptr(fd), "native-nitro-device")
	defer func() {
		if returned != nil {
			returned = errors.Join(returned, file.Close())
		}
	}()
	var actual unix.Stat_t
	if err := unix.Fstat(fd, &actual); err != nil {
		return nil, fmt.Errorf("inspect retained Nitro device: %w", err)
	}
	if actual.Dev != original.Dev || actual.Ino != original.Ino || actual.Rdev != original.Rdev || actual.Mode != original.Mode {
		return nil, errors.New("the native Nitro device changed while opening")
	}
	if err := CheckDescriptor(fd); err != nil {
		return nil, err
	}
	return file, nil
}

// Matches checks the kernel's misc-device number against the retained inode.
func Matches(mode uint32, device uint64, sysfs string) bool {
	majorText, minorText, ok := strings.Cut(strings.TrimSpace(sysfs), ":")
	if !ok {
		return false
	}
	major, e1 := strconv.ParseUint(majorText, 10, 32)
	minor, e2 := strconv.ParseUint(minorText, 10, 32)
	return e1 == nil && e2 == nil && major == 10 && mode&unix.S_IFMT == unix.S_IFCHR && uint64(unix.Major(device)) == major && uint64(unix.Minor(device)) == minor
}

// CheckDescriptor checks the SAME retained native descriptor before a jail
// captures it. It accepts only the fixed kernel device, never a file label.
func CheckDescriptor(fd int) error {
	var native unix.Stat_t
	if err := unix.Fstat(fd, &native); err != nil {
		return fmt.Errorf("inspect supplied Nitro descriptor: %w", err)
	}
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_PATH != 0 || flags&unix.O_ACCMODE != unix.O_RDWR {
		return errors.New("the Nitro device has no native read-write rights")
	}
	number, err := deviceNumber(Sysfs)
	if err != nil || !Matches(native.Mode, uint64(native.Rdev), string(number)) {
		return errors.New("the retained Nitro descriptor is not the fixed kernel device")
	}
	supported, err := unix.IoctlGetUint32(fd, getSupportedFlags)
	if err != nil {
		return fmt.Errorf("Failed to query hypervisor-supported flags: %w", err)
	}
	if supported&RequiredFlags != RequiredFlags {
		return errors.New("the parent lacks required Nitro PCIe and CPU overcommit flags")
	}
	return nil
}

func deviceNumber(base string) ([]byte, error) {
	file, err := os.Open(base + "/dev")
	if err != nil {
		return nil, fmt.Errorf("open Nitro sysfs identity: %w", err)
	}
	defer file.Close()
	var filesystem unix.Statfs_t
	if err := unix.Fstatfs(int(file.Fd()), &filesystem); err != nil || filesystem.Type != unix.SYSFS_MAGIC {
		return nil, errors.New("the Nitro device identity is not native sysfs")
	}
	data, err := io.ReadAll(io.LimitReader(file, 65))
	if err != nil || len(data) > 64 {
		return nil, errors.New("the native Nitro device number is unavailable or too large")
	}
	return data, nil
}
