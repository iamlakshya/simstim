package cachefs

import "os"

// RenameNativeNoReplace moves single native leaves without replacing a
// destination. Unix user worktrees can contain names outside the portable
// cache-name vocabulary. Root ownership and no-clobber semantics are identical.
func RenameNativeNoReplace(root *os.Root, from, to string) error {
	if err := safeNativeLeaf(from); err != nil {
		return err
	}
	if err := safeNativeLeaf(to); err != nil {
		return err
	}
	if err := validateRoot(root, nil); err != nil {
		return err
	}
	return renameNoReplaceNative(root, from, to)
}
