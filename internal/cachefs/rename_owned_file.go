package cachefs

import (
	"fmt"
	"io/fs"
	"os"
)

// RenameOwnedFileNoReplace captures one descriptor-owned regular file under a
// safe ancestor, including a public systemd user directory. It grants no file
// reading or deletion authority. The caller must check the captured inode and
// contents again before cleanup; a raced substitution must be preserved.
func RenameOwnedFileNoReplace(parent *os.Root, file *os.File, from, to string, mode fs.FileMode) error {
	if err := safeLeaf(from); err != nil {
		return err
	}
	if err := safeLeaf(to); err != nil {
		return err
	}
	if err := validateRootAs(parent, nil, false); err != nil {
		return err
	}
	info, err := ValidateDescriptor(file, mode)
	if err != nil {
		return err
	}
	named, err := parent.Lstat(from)
	if err != nil || !named.Mode().IsRegular() || !owned(named) || !os.SameFile(info, named) || !modeMatches(named, mode) || !descriptorSafe(file, named) {
		return fmt.Errorf("the owned file changed before capture")
	}
	return renameNoReplaceNative(parent, from, to)
}

// RenameOwnedSymlinkNoReplace captures an owned link beneath a safe ancestor.
// The caller retains its original native reference and target and must check
// both after the capture before unlinking or claiming a publication.
func RenameOwnedSymlinkNoReplace(parent *os.Root, from, to string) error {
	if err := safeLeaf(from); err != nil {
		return err
	}
	if err := safeLeaf(to); err != nil {
		return err
	}
	if err := validateRootAs(parent, nil, false); err != nil {
		return err
	}
	info, err := parent.Lstat(from)
	if err != nil || info.Mode()&os.ModeSymlink == 0 || !owned(info) {
		return fmt.Errorf("the owned symlink changed before capture")
	}
	return renameNoReplaceNative(parent, from, to)
}
