//go:build linux

package host

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/brand"
	"golang.org/x/sys/unix"
)

// readMemoryUnitV1 reads the canonical unit directly from the native cgroup2
// filesystem. Saved unit names, sockets and guest reports choose no path or
// value here. Unknown native reads refuse admission instead of returning zero.
func readMemoryUnitV1(ctx context.Context, id string, walled bool) (memoryUnitFactsV1, error) {
	var facts memoryUnitFactsV1
	if !validID(id) {
		return facts, sessionLeaseError("the memory reservation has no canonical primary ID")
	}
	if err := ctx.Err(); err != nil {
		return facts, err
	}
	root, err := unix.Open(cgroupRoot, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return facts, memoryNativeFailureV1(err)
	}
	defer unix.Close(root)
	var filesystem unix.Statfs_t
	if err := unix.Fstatfs(root, &filesystem); err != nil || filesystem.Type != unix.CGROUP2_SUPER_MAGIC {
		return facts, memoryNativeFailureV1(err)
	}
	uid := os.Getuid()
	name := fmt.Sprintf("user.slice/user-%d.slice/user@%d.service/%s/%s", uid, uid, brand.Slice, UnitName(id))
	fd, err := unix.Openat2(root, name, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if errors.Is(err, unix.ENOENT) {
		return facts, nil
	}
	if err != nil {
		return facts, memoryNativeFailureV1(err)
	}
	defer unix.Close(fd)
	facts.exists = true
	var before unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil {
		return facts, memoryNativeFailureV1(err)
	}
	leaf := "memory.current"
	if walled {
		leaf = "memory.max"
	}
	value, err := readNativeMemoryValueV1(fd, leaf)
	if err != nil || walled && value == 0 {
		return facts, memoryNativeFailureV1(err)
	}
	if walled {
		facts.maximum = value
		facts.swap, err = readNativeMemoryValueV1(fd, "memory.swap.max")
		if err != nil {
			return facts, memoryNativeFailureV1(err)
		}
	} else {
		facts.current = value
	}
	current, err := unix.Openat2(root, name, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return facts, memoryNativeFailureV1(err)
	}
	var after unix.Stat_t
	statErr := unix.Fstat(current, &after)
	closeErr := unix.Close(current)
	if statErr != nil || closeErr != nil || before.Dev != after.Dev || before.Ino != after.Ino {
		return facts, memoryNativeFailureV1(errors.Join(statErr, closeErr))
	}
	if err := ctx.Err(); err != nil {
		return facts, err
	}
	return facts, nil
}

func memoryNativeFailureV1(cause error) error {
	return errors.Join(UserError("no_room", "the native canonical cgroup memory facts are unavailable or changed", "Inspect the cgroup v2 memory controller and retry admission through the same host."), cause)
}

// Every value is read from the SAME already-open canonical cgroup object.
func readNativeMemoryValueV1(directory int, leaf string) (int64, error) {
	fd, err := unix.Openat2(directory, leaf, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return 0, err
	}
	file := os.NewFile(uintptr(fd), "native-unit-memory")
	data, readErr := io.ReadAll(io.LimitReader(file, 65))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(data) > 64 {
		return 0, errors.Join(readErr, closeErr, fmt.Errorf("native memory value is unreadable or exceeds its bound"))
	}
	text := strings.TrimSuffix(string(data), "\n")
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil || value < 0 || strconv.FormatInt(value, 10) != text {
		return 0, errors.Join(err, fmt.Errorf("native memory value has no exact nonnegative byte count"))
	}
	return value, nil
}

// memoryUnitIDsV1 enumerates only native canonical session units. It catches a
// real unit whose entire manifest directory is missing, which a directory-only
// reservation scan could otherwise count as zero. Its numbers are facts only.
func memoryUnitIDsV1(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := unix.Open(cgroupRoot, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, memoryNativeFailureV1(err)
	}
	defer unix.Close(root)
	var filesystem unix.Statfs_t
	if err := unix.Fstatfs(root, &filesystem); err != nil || filesystem.Type != unix.CGROUP2_SUPER_MAGIC {
		return nil, memoryNativeFailureV1(err)
	}
	name := fmt.Sprintf("user.slice/user-%d.slice/user@%d.service/%s", os.Getuid(), os.Getuid(), brand.Slice)
	directory, err := unix.Openat2(root, name, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if errors.Is(err, unix.ENOENT) {
		return nil, nil
	}
	if err != nil {
		return nil, memoryNativeFailureV1(err)
	}
	file := os.NewFile(uintptr(directory), "native-canonical-session-units")
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return nil, memoryNativeFailureV1(err)
	}
	entries, err := file.ReadDir(-1)
	if err != nil {
		return nil, memoryNativeFailureV1(err)
	}
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		unit := entry.Name()
		if !strings.HasPrefix(unit, brand.UnitPrefix) {
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(unit, brand.UnitPrefix), ".service")
		if !validID(id) || UnitName(id) != unit || !entry.IsDir() {
			return nil, memoryNativeFailureV1(errors.New("a canonical native session unit has an unknown identity"))
		}
		result = append(result, id)
	}
	current, err := unix.Openat2(root, name, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return nil, memoryNativeFailureV1(err)
	}
	other := os.NewFile(uintptr(current), "current-canonical-session-units")
	after, statErr := other.Stat()
	closeErr := other.Close()
	if statErr != nil || closeErr != nil || !os.SameFile(before, after) {
		return nil, memoryNativeFailureV1(errors.Join(statErr, closeErr))
	}
	return result, nil
}
