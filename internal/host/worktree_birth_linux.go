//go:build linux

package host

import (
	"bytes"
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// Called only with the actual freshly created native directory producer's
// retained root. Filesystems without a stable native handle leave no birth
// receipt; ordinary worktree use continues, but that data grants no deletion.
func captureWorktreeBirthV2(root *os.Root) (result *worktreeBirthV2, returned error) {
	if root == nil {
		return nil, placementError("the worktree birth has no actual native directory")
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
		return nil, placementError("the original worktree changed its actual filesystem during native birth capture")
	}
	birth := &worktreeBirthV2{Version: 2, Filesystem: int64(before.Type), FSID: before.Fsid.Val, Type: handle.Type(), Handle: bytes.Clone(handle.Bytes())}
	if !birth.knownV2() {
		return nil, placementError("the actual filesystem returned an unknown worktree generation handle")
	}
	return birth, nil
}

func checkWorktreeBirthV2(root *os.Root, birth *worktreeBirthV2) error {
	if !birth.knownV2() {
		return UserError("worktree_origin", "the worktree has no original native filesystem generation receipt", "Save this worktree and inspect its original creation and filesystem support before removing it by hand. Force does not establish ownership.")
	}
	actual, err := captureWorktreeBirthV2(root)
	if err != nil {
		return err
	}
	if actual == nil || actual.Filesystem != birth.Filesystem || actual.FSID != birth.FSID || actual.Type != birth.Type || !bytes.Equal(actual.Handle, birth.Handle) {
		return UserError("worktree_origin", "the actual worktree directory differs from its original native creation generation", "Preserve this directory and inspect the original worktree creation record before retrying removal. Force does not establish ownership.")
	}
	return nil
}
