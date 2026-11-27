package cachefs

import "os"

// RenameNoReplace atomically moves one leaf to another without replacing an
// existing destination. Both names stay beneath an anchored owned root.
// Native support is required. A refused move leaves both names untouched.
func RenameNoReplace(root *os.Root, from, to string) error {
	if err := safeLeaf(from); err != nil {
		return err
	}
	if err := safeLeaf(to); err != nil {
		return err
	}
	if err := validateRoot(root, nil); err != nil {
		return err
	}
	return renameNoReplaceNative(root, from, to)
}

// RenameBetweenNoReplace moves checked leaves between two anchored owned roots.
// It requires a native atomic no-replace operation and never falls back to copy
// or replacement when the roots are on different filesystems.
func RenameBetweenNoReplace(fromRoot *os.Root, from string, toRoot *os.Root, to string) error {
	if err := safeLeaf(from); err != nil {
		return err
	}
	if err := safeLeaf(to); err != nil {
		return err
	}
	if err := validateRoot(fromRoot, nil); err != nil {
		return err
	}
	if err := validateRoot(toRoot, nil); err != nil {
		return err
	}
	return renameBetweenNoReplaceNative(fromRoot, from, toRoot, to)
}
func renameNoReplaceNative(root *os.Root, from, to string) error {
	return renameBetweenNoReplaceNative(root, from, root, to)
}
