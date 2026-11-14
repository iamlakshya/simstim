package cachefs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// ModeMatches reports only the native permission representation. It does not
// establish owner or identity authority. Use validation for security checks.
func ModeMatches(info fs.FileInfo, mode fs.FileMode) bool {
	return info != nil && modeMatches(info, mode)
}

// ValidateDescriptor checks the actual regular single-link descriptor, its
// mode and native owner/ACL/reparse policy. It never modifies the file.
func ValidateDescriptor(file *os.File, mode fs.FileMode) (os.FileInfo, error) {
	if mode & ^fs.FileMode(0o777) != 0 || mode&0o022 != 0 {
		return nil, fmt.Errorf("cache file mode is not private")
	}
	if file == nil {
		return nil, fmt.Errorf("cache descriptor is missing")
	}
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat cache descriptor: %w", err)
	}
	if !info.Mode().IsRegular() || !owned(info) || !modeMatches(info, mode) || !descriptorSafe(file, info) {
		return nil, fmt.Errorf("cache descriptor is not an owned regular file with the expected mode and one link")
	}
	return info, nil
}

// ValidateFile adds the current anchored leaf identity to descriptor checks.
// A substituted name or unsafe descriptor is refused without any cleanup.
func ValidateFile(root *os.Root, leaf string, file *os.File, mode fs.FileMode) (os.FileInfo, error) {
	if err := safeLeaf(leaf); err != nil {
		return nil, err
	}
	if err := validateRoot(root, nil); err != nil {
		return nil, err
	}
	info, err := ValidateDescriptor(file, mode)
	if err != nil {
		return nil, err
	}
	current, err := root.Lstat(leaf)
	if err != nil {
		return nil, fmt.Errorf("inspect cache leaf identity: %w", err)
	}
	if !current.Mode().IsRegular() || !owned(current) || !modeMatches(current, mode) || !os.SameFile(info, current) || !descriptorSafe(file, current) {
		return nil, fmt.Errorf("cache leaf changed identity or ownership")
	}
	return info, nil
}

// OpenFile accepts a single leaf and only ordinary access/create/append flags.
// O_TRUNC is always refused. A caller may truncate the verified descriptor
// afterward. Creation is exclusive, and existing objects are never modified.
func OpenFile(root *os.Root, leaf string, flags int, mode fs.FileMode) (*os.File, error) {
	if err := safeLeaf(leaf); err != nil {
		return nil, err
	}
	const allowed = os.O_WRONLY | os.O_RDWR | os.O_APPEND | os.O_CREATE | os.O_EXCL
	if flags&os.O_TRUNC != 0 || flags & ^allowed != 0 || flags&(os.O_WRONLY|os.O_RDWR) == (os.O_WRONLY|os.O_RDWR) || flags&os.O_EXCL != 0 && flags&os.O_CREATE == 0 {
		return nil, fmt.Errorf("cache open flags are unsupported or would truncate before validation")
	}
	if mode & ^fs.FileMode(0o777) != 0 || mode&0o022 != 0 {
		return nil, fmt.Errorf("cache file mode contains unsupported bits")
	}
	if err := validateRoot(root, nil); err != nil {
		return nil, err
	}
	if flags&os.O_CREATE != 0 {
		file, err := openNoFollow(root, leaf, flags|os.O_EXCL, mode)
		if err == nil {
			if _, err := ValidateFile(root, leaf, file, mode); err != nil {
				file.Close()
				return nil, err
			}
			return file, nil
		}
		if flags&os.O_EXCL != 0 || !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("create cache leaf: %w", err)
		}
	}
	before, err := root.Lstat(leaf)
	if err != nil {
		return nil, fmt.Errorf("inspect cache leaf: %w", err)
	}
	if !before.Mode().IsRegular() || !owned(before) || !modeMatches(before, mode) {
		return nil, fmt.Errorf("existing cache leaf is unsafe")
	}
	file, err := openNoFollow(root, leaf, flags & ^(os.O_CREATE|os.O_EXCL), 0)
	if err != nil {
		return nil, fmt.Errorf("open cache leaf: %w", err)
	}
	info, err := ValidateFile(root, leaf, file, mode)
	if err != nil {
		file.Close()
		return nil, err
	}
	if !os.SameFile(before, info) {
		file.Close()
		return nil, fmt.Errorf("cache leaf changed during open")
	}
	return file, nil
}
