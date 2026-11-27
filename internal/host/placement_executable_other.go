//go:build !linux

package host

import (
	"os"
	"path/filepath"
)

// Non-Linux hosts are used only by source tests. Production hosts target Linux.
func mappedExecutable() (string, os.FileInfo, error) {
	name, err := os.Executable()
	if err != nil {
		return "", nil, placementError("cannot identify the running executable")
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(name))
	if err != nil {
		return "", nil, placementError("cannot resolve the running executable's directory")
	}
	name = filepath.Join(dir, filepath.Base(name))
	info, err := os.Lstat(name)
	if err != nil {
		return "", nil, placementError("cannot anchor the running executable")
	}
	current, err := mappedExecutableMatches(name, info)
	return name, current, err
}
