package image

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

func openPrivateRoot(dir string) (*os.Root, error) { return cachefs.OpenPrivateRoot(dir, true) }

// childRoot walks only checked private directories beneath the pinned store.
// cachefs owns every OS-specific owner/ACL/no-follow and descriptor rule.
func (s *Store) childRoot(name string) (*os.Root, error) {
	if !fs.ValidPath(name) || strings.Contains(name, "\\") {
		return nil, fmt.Errorf("image folder is not a clean store-relative path")
	}
	root, err := s.root.OpenRoot(".")
	if err != nil {
		return nil, fmt.Errorf("duplicate image anchor: %w", err)
	}
	if name == "." {
		return root, nil
	}
	for _, leaf := range strings.Split(name, "/") {
		next, err := cachefs.OpenPrivateChild(root, leaf, false)
		root.Close()
		if err != nil {
			return nil, err
		}
		root = next
	}
	return root, nil
}

func (s *Store) parent(name string) (*os.Root, string, error) {
	if !fs.ValidPath(name) || strings.Contains(name, "\\") {
		return nil, "", fmt.Errorf("image file is not a clean store-relative path")
	}
	root, err := s.childRoot(path.Dir(name))
	return root, path.Base(name), err
}

func (s *Store) mkdir(name string) error {
	root, leaf, err := s.parent(name)
	if err != nil {
		return err
	}
	defer root.Close()
	return cachefs.MakePrivateDir(root, leaf)
}
func (s *Store) regular(name string, flags int, mode os.FileMode) (*os.File, error) {
	root, leaf, err := s.parent(name)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return cachefs.OpenFile(root, leaf, flags, mode)
}

func (s *Store) renameNoReplace(from, to string) error {
	fromRoot, fromLeaf, err := s.parent(from)
	if err != nil {
		return err
	}
	defer fromRoot.Close()
	toRoot, toLeaf, err := s.parent(to)
	if err != nil {
		return err
	}
	defer toRoot.Close()
	return cachefs.RenameBetweenNoReplace(fromRoot, fromLeaf, toRoot, toLeaf)
}

func validateBlobDescriptor(file *os.File) (os.FileInfo, error) {
	st, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat image layer: %w", err)
	}
	mode := os.FileMode(0o444)
	if st.Mode().Perm()&0o222 != 0 {
		mode = 0o600
	}
	return cachefs.ValidateDescriptor(file, mode)
}
