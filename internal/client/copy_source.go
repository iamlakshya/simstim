package client

import (
	"path/filepath"
	"strings"
)

// copySourceParts keeps real link-sensitive parent traversal. Split does not
// clean the path. EvalSymlinks runs on that raw parent before Abs can clean an
// already resolved path. The returned containing directory is the grant root;
// an ordinary leaf alias must remain beneath it when os.Root opens the leaf.
func copySourceParts(source string) (parent, name, guestDefault string, err error) {
	rawParent, leaf := filepath.Split(source)
	if leaf == "" || leaf == "." || leaf == ".." {
		resolved, err := filepath.EvalSymlinks(source)
		if err != nil {
			return "", "", "", err
		}
		resolved, err = filepath.Abs(resolved)
		if err != nil {
			return "", "", "", err
		}
		if filepath.Dir(resolved) == resolved {
			return "", "", "", envInputError("bad_copy", "the filesystem root is not a copy source", "Choose an ordinary local file or folder.", KindUsage)
		}
		return filepath.Dir(resolved), filepath.Base(resolved), resolved, nil
	}
	if rawParent == "" {
		rawParent = "."
	}
	resolved, err := filepath.EvalSymlinks(rawParent)
	if err != nil {
		return "", "", "", err
	}
	parent, err = filepath.Abs(resolved)
	if err != nil {
		return "", "", "", err
	}
	guestDefault, err = filepath.Abs(source)
	if err != nil {
		return "", "", "", err
	}
	// Destination mirroring is not source authorisation. For dot traversal
	// use its actual resolved meaning, rather than mirror a lexical shortcut.
	for _, part := range strings.FieldsFunc(source, func(r rune) bool { return r == '/' || r == filepath.Separator }) {
		if part == ".." {
			resolved, err := filepath.EvalSymlinks(source)
			if err != nil {
				return "", "", "", err
			}
			guestDefault, err = filepath.Abs(resolved)
			if err != nil {
				return "", "", "", err
			}
			break
		}
	}
	return parent, leaf, guestDefault, nil
}
