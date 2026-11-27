package cachefs

import (
	"fmt"
	"io/fs"
	"os"
)

// ValidateOwnedFile validates one existing regular inode beneath a safe owned
// ancestor. It grants no directory/file creation or content ownership by name.
func ValidateOwnedFile(root *os.Root, leaf string, file *os.File, mode fs.FileMode) (os.FileInfo, error) {
	if err := safeLeaf(leaf); err != nil {
		return nil, err
	}
	if err := validateRootAs(root, nil, false); err != nil {
		return nil, err
	}
	info, err := ValidateDescriptor(file, mode)
	if err != nil {
		return nil, err
	}
	named, err := root.Lstat(leaf)
	if err != nil || !named.Mode().IsRegular() || !owned(named) || !modeMatches(named, mode) || !os.SameFile(info, named) || !descriptorSafe(file, named) {
		return nil, fmt.Errorf("the owned regular file changed its native named identity")
	}
	return info, nil
}

// OpenOwnedFileExisting opens a checked read-only descriptor under a safe
// ancestor. Unlike OpenFile it has no creation, append or write variant.
func OpenOwnedFileExisting(root *os.Root, leaf string, mode fs.FileMode) (*os.File, error) {
	if err := safeLeaf(leaf); err != nil {
		return nil, err
	}
	if err := validateRootAs(root, nil, false); err != nil {
		return nil, err
	}
	before, err := root.Lstat(leaf)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || !owned(before) || !modeMatches(before, mode) {
		return nil, fmt.Errorf("the existing owned file has an unsafe native type or mode")
	}
	file, err := openNoFollow(root, leaf, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	info, err := ValidateOwnedFile(root, leaf, file, mode)
	if err != nil || !os.SameFile(before, info) {
		_ = file.Close()
		return nil, fmt.Errorf("the existing owned file changed during open")
	}
	return file, nil
}
