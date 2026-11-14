// Package cachefs checks private cache roots and file descriptors. It is shared
// by cache owners without knowing their roles, manifests or directory layout.
package cachefs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// OpenPrivateRoot opens an owned cache root without creating storage-specific
// children or lock files. Existing directories are never chmodded. A false
// create argument performs no filesystem creation.
func OpenPrivateRoot(dir string, create bool) (*os.Root, error) {
	return openCheckedRoot(dir, create, true)
}

// OpenSafeAncestor opens an existing owned or system ancestor, including a
// root-owned sticky temporary directory. It creates nothing and grants no
// private-file trust. Callers must validate each owned child independently.
func OpenSafeAncestor(dir string) (*os.Root, error) { return openCheckedRoot(dir, false, false) }

func openCheckedRoot(dir string, create, private bool) (*os.Root, error) {
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("private cache root is not absolute")
	}
	volume := filepath.VolumeName(dir)
	ancestor, err := os.OpenRoot(volume + string(filepath.Separator))
	if err != nil {
		return nil, fmt.Errorf("open private cache ancestor: %w", err)
	}
	defer ancestor.Close()
	relative := strings.TrimPrefix(strings.TrimPrefix(filepath.Clean(dir), volume), string(filepath.Separator))
	parts := strings.Split(relative, string(filepath.Separator))
	name := ""
	var final os.FileInfo
	for i, part := range parts {
		name = filepath.Join(name, part)
		info, err := ancestor.Lstat(name)
		if create && errors.Is(err, os.ErrNotExist) {
			if err := ancestor.Mkdir(name, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return nil, fmt.Errorf("make private cache ancestor: %w", err)
			}
			info, err = ancestor.Lstat(name)
		}
		if err != nil {
			return nil, fmt.Errorf("inspect private cache ancestor: %w", err)
		}
		if !safeDirectory(ancestor, name, info, private && i == len(parts)-1) {
			return nil, fmt.Errorf("private cache root has an unsafe ancestor")
		}
		final = info
	}
	root, err := ancestor.OpenRoot(name)
	if err != nil {
		return nil, fmt.Errorf("anchor private cache root: %w", err)
	}
	if err := validateRootAs(root, final, private); err != nil {
		root.Close()
		return nil, err
	}
	return root, nil
}

func validateRoot(root *os.Root, before os.FileInfo) error {
	return validateRootAs(root, before, true)
}

func validateRootAs(root *os.Root, before os.FileInfo, private bool) error {
	if root == nil {
		return fmt.Errorf("private cache root is missing")
	}
	dir, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("inspect anchored private cache root: %w", err)
	}
	defer dir.Close()
	info, err := dir.Stat()
	if err != nil {
		return fmt.Errorf("stat anchored private cache root: %w", err)
	}
	if before != nil && !os.SameFile(before, info) {
		return fmt.Errorf("private cache root changed identity")
	}
	if !safeDirectory(root, ".", info, private) {
		return fmt.Errorf("anchored cache root is not privately owned")
	}
	return nil
}

// OpenPrivateChild opens one owned private child below a checked root. It
// refuses links, changed identity and unsafe existing modes without chmod.
func OpenPrivateChild(root *os.Root, leaf string, create bool) (*os.Root, error) {
	if err := safeLeaf(leaf); err != nil {
		return nil, err
	}
	if err := validateRoot(root, nil); err != nil {
		return nil, err
	}
	if create {
		if err := root.Mkdir(leaf, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("make private cache child: %w", err)
		}
	}
	info, err := root.Lstat(leaf)
	if err != nil {
		return nil, fmt.Errorf("inspect private cache child: %w", err)
	}
	if !safeDirectory(root, leaf, info, true) || !privateMode(info) {
		return nil, fmt.Errorf("private cache child is unsafe")
	}
	child, err := root.OpenRoot(leaf)
	if err != nil {
		return nil, fmt.Errorf("anchor private cache child: %w", err)
	}
	if err := validateRoot(child, info); err != nil {
		child.Close()
		return nil, err
	}
	return child, nil
}

// MakePrivateDir creates or validates one private child, without replacing or
// chmodding an existing directory.
func MakePrivateDir(root *os.Root, leaf string) error {
	child, err := OpenPrivateChild(root, leaf, true)
	if err != nil {
		return err
	}
	if err := child.Close(); err != nil {
		return fmt.Errorf("close private cache child: %w", err)
	}
	return nil
}

func safeLeaf(name string) error {
	if name == "" || name == "." || name == ".." || filepath.IsAbs(name) || filepath.VolumeName(name) != "" || strings.ContainsAny(name, "/\\\x00:") || strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return fmt.Errorf("cache entry must be one safe leaf name")
	}
	base := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	switch base {
	case "CON", "PRN", "AUX", "NUL":
		return fmt.Errorf("cache leaf is a reserved device name")
	}
	if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9' {
		return fmt.Errorf("cache leaf is a reserved device name")
	}
	return nil
}
