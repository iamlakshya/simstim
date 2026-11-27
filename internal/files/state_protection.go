package files

import (
	"fmt"
	"path"
	"slices"
	"strings"
)

// ProtectedPaths derives guest state exclusions from the SAME persistence
// plan and family suffix definition. A copied file must not overwrite a WAL,
// SHM or journal separately from its protected main file.
func (p StatePlan) ProtectedPaths(home string) ([]string, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if !path.IsAbs(home) || path.Clean(home) != home || strings.ContainsAny(home, "\\\x00\r\n") {
		return nil, fmt.Errorf("invalid guest state home")
	}
	var paths []string
	for _, name := range p.Dirs {
		paths = append(paths, path.Join(home, name))
	}
	for _, name := range p.Files {
		for _, suffix := range sidecars {
			paths = append(paths, path.Join(home, name+suffix))
		}
	}
	slices.Sort(paths)
	return slices.Compact(paths), nil
}
