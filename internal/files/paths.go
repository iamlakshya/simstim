// Package files defines the files that cross a sandbox's files door.
package files

import (
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"unicode/utf8"
)

// RestorePath is the read-only generation that the guest may restore.
const RestorePath = "/mnt/.agent-state-restore"

// CapturePath is the private incoming generation for this run alone.
const CapturePath = "/mnt/.agent-state-capture"

// StatePlan separates in-place folders from copy-backed file families.
// The agent table's public StateDirs still includes both kinds of path.
type StatePlan struct {
	// Dirs are private directory binds that stay in place.
	Dirs []string `json:"dirs,omitempty"`
	// Files are complete copied families published only after a clean exit.
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

// Relative reports whether name is a clean source or state path below a
// root, excluding Git metadata. Rooted metadata reads use OpenRegular.
func Relative(name string) bool {
	if !relativePath(name) {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".git" {
			return false
		}
	}
	return true
}

func relativePath(name string) bool {
	return utf8.ValidString(name) && name != "" && name != "." && name != ".." && !path.IsAbs(name) && path.Clean(name) == name && !strings.HasPrefix(name, "../") && !strings.ContainsAny(name, "\\\x00\r\n")
}

func forbiddenState(name string) bool {
	for _, part := range strings.Split(name, "/") {
		switch part {
		case ".bashrc", ".profile", ".bash_profile", ".bash_login", ".bash_logout", ".zshrc", ".zprofile", ".zshenv", ".zlogin", ".zlogout", ".cshrc", ".tcshrc", ".kshrc", ".login", ".logout", ".gitconfig", ".config", "hooks", "settings.json", "config.toml":
			return true
		}
	}
	return false
}

// OpenRegular opens only a regular descriptor below root, without following
// any link component or blocking on a special file. The descriptor pins the
// object actually opened, so a later path replacement cannot redirect reads.
func OpenRegular(root *os.Root, name string) (*os.File, error) {
	if !relativePath(name) {
		return nil, fmt.Errorf("unsafe relative file path")
	}
	f, err := openRegularFile(root, name)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("file is not regular")
	}
	return f, nil
}

func copyRegular(src, dst *os.Root, name string) error {
	f, err := OpenRegular(src, name)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = dst.MkdirAll(path.Dir(name), 0700); err != nil {
		return err
	}
	out, err := dst.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, f)
	if err == nil {
		err = out.Sync()
	}
	err = errorsClose(err, out)
	if err != nil {
		return err
	}
	for folder := path.Dir(name); folder != "."; folder = path.Dir(folder) {
		parent, err := dst.Open(folder)
		if err != nil {
			return err
		}
		err = parent.Sync()
		if err = errorsClose(err, parent); err != nil {
			return err
		}
	}
	return nil
}

func errorsClose(err error, f *os.File) error {
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func syncRoot(root *os.Root) error {
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
