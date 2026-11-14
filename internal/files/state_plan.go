// Package files defines the files that cross a sandbox's files door.
package files

import (
	"fmt"
	"path"
	"strings"
)

// RestorePath is the read-only generation that the guest may restore.
const RestorePath = "/mnt/.agent-state-restore"

// CapturePath is the private incoming generation for this run alone.
const CapturePath = "/mnt/.agent-state-capture"

// StatePlan separates in-place folders from copy-backed file families.
// The agent table's public StateDirs still includes both kinds of path.
type StatePlan struct {
	Dirs  []string `json:"dirs,omitempty"`
	Files []string `json:"files,omitempty"`
}

// Validate checks every path and refuses ambiguous or executable state.
func (p StatePlan) Validate() error {
	var seen []string
	for _, group := range [][]string{p.Dirs, p.Files} {
		for _, name := range group {
			if !Relative(name) || forbiddenState(name) {
				return fmt.Errorf("unsafe kept state path %q", name)
			}
			for _, old := range seen {
				if name == old || strings.HasPrefix(name, old+"/") || strings.HasPrefix(old, name+"/") {
					return fmt.Errorf("overlapping kept state paths")
				}
			}
			seen = append(seen, name)
		}
	}
	return nil
}

// Relative reports whether name is a clean path below one root.
func Relative(name string) bool {
	return name != "" && name != "." && name != ".." && !path.IsAbs(name) && path.Clean(name) == name && !strings.HasPrefix(name, "../") && !strings.ContainsAny(name, "\\\x00\r\n") && !strings.Contains(name, "/.git/") && name != ".git" && !strings.HasPrefix(name, ".git/")
}

func forbiddenState(name string) bool {
	for _, part := range strings.Split(name, "/") {
		switch part {
		case ".bashrc", ".profile", ".bash_profile", ".bash_login", ".zshrc", ".zprofile", ".zshenv", ".zlogin", ".config", "hooks", "settings.json", "config.toml":
			return true
		}
	}
	return false
}
