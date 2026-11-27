//go:build linux

package fsbirth

import (
	"bytes"
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// Capture observes only the actual held native directory. Unsupported
// filesystems yield nil data, without upgrading the directory or granting
// removal permission. It never opens an object by handle or requests a cap.
func Capture(root *os.Root) (result *DataV2, returned error) {
	if root == nil {
		return nil, errors.New("native filesystem generation has no actual directory")
	}
	file, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() { returned = errors.Join(returned, file.Close()) }()
	var before, after unix.Statfs_t
	if err := unix.Fstatfs(int(file.Fd()), &before); err != nil {
		return nil, err
	}
	switch before.Type {
	case unix.EXT4_SUPER_MAGIC, unix.XFS_SUPER_MAGIC, unix.BTRFS_SUPER_MAGIC:
	default:
		return nil, nil
	}
	handle, _, err := unix.NameToHandleAt(int(file.Fd()), ".", 0)
	if errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOSYS) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := unix.Fstatfs(int(file.Fd()), &after); err != nil {
		return nil, err
	}
	if before.Type != after.Type || before.Fsid != after.Fsid {
		return nil, errors.New("the native directory changed filesystem during generation capture")
	}
	data := &DataV2{Version: 2, Filesystem: int64(before.Type), FSID: before.Fsid.Val, Type: handle.Type(), Handle: bytes.Clone(handle.Bytes())}
	if !data.Known() {
		return nil, errors.New("the filesystem returned an unknown native generation handle")
	}
	return data, nil
}
