package files

import (
	"fmt"
	"os"
	"path/filepath"
)

// OpenContainedRegular opens a regular file through root. Ordinary links
// may remain inside it. Escapes fail in os.Root, and the descriptor must
// still name the object that was inspected before opening.
func OpenContainedRegular(root *os.Root, name string) (*os.File, error) {
	if !relativePath(name) {
		return nil, fmt.Errorf("unsafe relative file path")
	}
	st, err := root.Stat(name)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("file is not regular")
	}
	file, err := root.OpenFile(name, includedFlags(), 0)
	if err != nil {
		return nil, err
	}
	actual, err := file.Stat()
	if err != nil || !actual.Mode().IsRegular() || !os.SameFile(st, actual) {
		file.Close()
		return nil, fmt.Errorf("file changed while opening")
	}
	return file, nil
}

// OpenContainedDirectory pins a directory through root without blocking if
// a raced replacement is a special file. Ordinary contained links are allowed.
func OpenContainedDirectory(root *os.Root, name string) (*os.File, error) {
	if name != "." && !relativePath(name) {
		return nil, fmt.Errorf("unsafe relative directory path")
	}
	want, err := root.Stat(name)
	if err != nil || !want.IsDir() {
		return nil, fmt.Errorf("source is not a directory")
	}
	file, err := root.OpenFile(name, includedFlags(), 0)
	if err != nil {
		return nil, err
	}
	got, err := file.Stat()
	if err != nil || !got.IsDir() || !os.SameFile(want, got) {
		file.Close()
		return nil, fmt.Errorf("directory changed while opening")
	}
	return file, nil
}

// OpenIncluded opens an explicitly chosen regular file through the source
// repository root. Contained links are allowed, except into Git metadata.
func OpenIncluded(root *os.Root, name string) (*os.File, error) {
	if !Relative(name) {
		return nil, fmt.Errorf("unsafe include path")
	}
	file, err := OpenContainedRegular(root, name)
	if err != nil {
		return nil, err
	}
	if err = includeResolvedPath(root, name); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func includeResolvedPath(root *os.Root, name string) error {
	base, err := filepath.EvalSymlinks(root.Name())
	if err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(base, filepath.FromSlash(name)))
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(base, resolved)
	if err != nil || !Relative(filepath.ToSlash(relative)) {
		return fmt.Errorf("include resolves outside source files or into Git metadata")
	}
	return nil
}

// CheckIncludeParent refuses an include destination whose parent alias
// resolves into the clone's Git metadata. A final symlink is replaced by
// rename, never followed.
func CheckIncludeParent(root *os.Root, name string) error {
	if !Relative(name) {
		return fmt.Errorf("unsafe include destination")
	}
	parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(name)))
	if parent == "." {
		return nil
	}
	return includeResolvedPath(root, parent)
}
