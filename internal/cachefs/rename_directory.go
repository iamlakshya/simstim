package cachefs

import (
	"fmt"
	"os"
)

// RenameOwnedDirectoryNoReplace captures an owned directory beneath a safe
// ancestor such as the root-owned sticky temporary directory. It grants no
// regular-file access through that ancestor. The caller verifies the captured
// directory reference before any empty-only removal or no-clobber restoration.
func RenameOwnedDirectoryNoReplace(parent *os.Root, from, to string) error {
	if err := safeNativeLeaf(from); err != nil {
		return err
	}
	if err := safeNativeLeaf(to); err != nil {
		return err
	}
	file, err := parent.Open(".")
	if err != nil {
		return err
	}
	info, err := file.Stat()
	file.Close()
	if err != nil || !safeDirectory(parent, ".", info, false) {
		return fmt.Errorf("directory parent is not a safe owned or system ancestor")
	}
	child, err := parent.Lstat(from)
	if err != nil || !safeDirectory(parent, from, child, true) {
		return fmt.Errorf("directory capture source is not safely owned")
	}
	return renameNoReplaceNative(parent, from, to)
}
