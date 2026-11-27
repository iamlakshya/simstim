//go:build linux

package runtimecache

import (
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// Link the actual retained read-only alias, never an incoming name that could be replaced after
// validation. The native procfs alias follows this same-inode file object.
func linkRuntimePartV1(part *Part, target string) (returned error) {
	name, valid := strings.CutPrefix(target, "sha256/")
	if !valid || name != part.item.SHA256 || part.reader == nil {
		return fmt.Errorf("runtime publication has another original target")
	}
	proc, err := unix.Open("/proc/self/fd", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open native runtime writer directory: %w", err)
	}
	defer func() { returned = errors.Join(returned, unix.Close(proc)) }()
	var filesystem unix.Statfs_t
	if err := unix.Fstatfs(proc, &filesystem); err != nil {
		return err
	}
	if filesystem.Type != unix.PROC_SUPER_MAGIC {
		return fmt.Errorf("runtime writer directory is not native procfs")
	}
	destination, err := part.store.root.Open("sha256")
	if err != nil {
		return err
	}
	part.publicationDir = destination // SAME Part owns this even when link/cleanup fails.
	err = unix.Linkat(proc, strconv.FormatUint(uint64(part.reader.Fd()), 10), int(destination.Fd()), name, unix.AT_SYMLINK_FOLLOW)
	part.linked = err == nil
	runtime.KeepAlive(part.reader)
	return err
}
