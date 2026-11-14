package client

import (
	"os"
	"path/filepath"
)

// keptConfigs lists the SSH configs of saved hosts. A configured path can
// reach a generated file through a symlink or a case-insensitive spelling.
func keptConfigs(hosts []Host) []string {
	paths := make([]string, 0, 2*len(hosts))
	for _, h := range hosts {
		paths = append(paths, wrapperPath(h))
		if h.SSHConfig != "" {
			paths = append(paths, filepath.Clean(expand(h.SSHConfig)))
		}
	}
	return paths
}

// usesConfig checks both path spelling and file identity. The latter also
// covers a linked folder, a hard link and case-insensitive file systems.
func usesConfig(path string, keep []string) bool {
	info, err := os.Stat(path)
	for _, p := range keep {
		if filepath.Clean(path) == filepath.Clean(p) {
			return true
		}
		if other, e := os.Stat(p); err == nil && e == nil && os.SameFile(info, other) {
			return true
		}
	}
	return false
}
