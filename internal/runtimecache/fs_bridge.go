package runtimecache

import (
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// cacheParent translates only storage-constructed paths into checked child
// roots and one leaf. The shared filesystem API accepts no multi-part path.
func cacheParent(root *os.Root, name string) (*os.Root, string, func(), error) {
	if strings.ContainsAny(name, "\\\x00") {
		return nil, "", nil, fmt.Errorf("runtime cache path is unsafe")
	}
	parts := strings.Split(name, "/")
	current := root
	var opened []*os.Root
	closeRoots := func() {
		for i := len(opened) - 1; i >= 0; i-- {
			_ = opened[i].Close()
		}
	}
	for _, part := range parts[:len(parts)-1] {
		child, err := cachefs.OpenPrivateChild(current, part, false)
		if err != nil {
			closeRoots()
			return nil, "", nil, err
		}
		opened = append(opened, child)
		current = child
	}
	return current, parts[len(parts)-1], closeRoots, nil
}

func openNoFollow(root *os.Root, name string, flags int, mode fs.FileMode) (*os.File, error) {
	parent, leaf, closeRoots, err := cacheParent(root, name)
	if err != nil {
		return nil, err
	}
	defer closeRoots()
	return cachefs.OpenFile(parent, leaf, flags, mode)
}

// These diagnostic and lease adapters delegate policy to the one shared
// implementation. No UID or Windows ACL decision is reimplemented here.
func modeMatches(info fs.FileInfo, mode fs.FileMode) bool { return cachefs.ModeMatches(info, mode) }
func descriptorSafe(file *os.File, before fs.FileInfo) bool {
	if before == nil {
		return false
	}
	info, err := cachefs.ValidateDescriptor(file, before.Mode().Perm())
	return err == nil && os.SameFile(before, info)
}
func tryLock(file *os.File) (bool, error) { return cachefs.TryLock(file, cachefs.Exclusive) }
func syncDirectory(file *os.File) error   { return cachefs.SyncDirectory(file) }
