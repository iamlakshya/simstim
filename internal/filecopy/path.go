// Package filecopy implements rooted file copies without archive extraction.
// Wire declarations and session authority belong to their actual callers.
package filecopy

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
)

// Error is a low-level copy failure; the real RPC boundary preserves its
// code/message/hint in its existing protocol error type.
type Error struct{ Code, Message, Hint string }

func (e *Error) Error() string { return e.Message + ". Try: " + e.Hint }

func failure(code, message string) error {
	return &Error{Code: code, Message: message, Hint: "Use ordinary paths without links or Git metadata, and review the selected files before copying again."}
}

// Name checks one actual filesystem component, never a cleaned replacement.
func Name(name string) error {
	if name == "" || name == "." || name == ".." || !utf8.ValidString(name) || strings.ContainsAny(name, "/\\") || api.CleanText(name, len(name)) != name {
		return failure("cp_path", "a copy entry has an unsafe or ambiguous name")
	}
	if strings.EqualFold(name, ".git") {
		return failure("cp_git", "a copy destination contains a Git metadata folder")
	}
	return platformName(name)
}

// Relative checks every component before any path operation. It never erases
// dot-dot or turns an unsafe guest name into a different laptop path.
func Relative(name string) error {
	if name == "" || path.IsAbs(name) || path.Clean(name) != name {
		return failure("cp_path", "a copy entry is not an ordinary relative path")
	}
	for _, part := range strings.Split(name, "/") {
		if err := Name(part); err != nil {
			return err
		}
	}
	return nil
}

// GuestPath expands only the public ~/ shorthand, then selects one fixed
// top-level root. Guest code receives the expanded absolute path.
func GuestPath(name string) (top, relative string, err error) {
	if strings.HasPrefix(name, "~/") {
		name = "/home/agent/" + strings.TrimPrefix(name, "~/")
	}
	if !path.IsAbs(name) || path.Clean(name) != name {
		return "", "", failure("cp_path", "the sandbox copy path must be absolute or start with ~/")
	}
	for _, candidate := range []string{"/work", "/home/agent", "/tmp", "/mnt"} {
		if name == candidate {
			return candidate, ".", nil
		}
		if strings.HasPrefix(name, candidate+"/") {
			relative = strings.TrimPrefix(name, candidate+"/")
			if err := Relative(relative); err != nil {
				return "", "", err
			}
			return candidate, relative, nil
		}
	}
	return "", "", failure("cp_path", "the sandbox copy path is outside /work, /home/agent, /tmp and /mnt")
}

// OpenLocalParent checks every laptop directory from the actual volume root.
// No symlink, junction or link-sensitive dot-dot is followed. The returned
// root pins the actual parent object used by every subsequent operation.
func OpenLocalParent(name string) (_ *os.Root, leaf string, err error) {
	for _, part := range strings.FieldsFunc(name, func(r rune) bool { return r == '/' || r == filepath.Separator }) {
		if part == ".." {
			return nil, "", failure("cp_path", "a laptop copy path contains a dot-dot component")
		}
	}
	absolute, err := filepath.Abs(name)
	if err != nil {
		return nil, "", err
	}
	leaf = filepath.Base(absolute)
	if err := Name(leaf); err != nil {
		return nil, "", err
	}
	top := filepath.VolumeName(absolute) + string(filepath.Separator)
	root, err := os.OpenRoot(top)
	if err != nil {
		return nil, "", err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, root.Close())
		}
	}()
	parent := strings.TrimPrefix(filepath.Dir(absolute), top)
	if parent == "" || parent == "." {
		return root, leaf, nil
	}
	for _, part := range strings.Split(parent, string(filepath.Separator)) {
		if err := Name(part); err != nil {
			return nil, "", err
		}
		info, statErr := root.Lstat(part)
		if statErr != nil {
			return nil, "", statErr
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, "", failure("cp_path", "a laptop copy parent is a link or is not a directory")
		}
		next, openErr := root.OpenRoot(part)
		if openErr != nil {
			return nil, "", openErr
		}
		opened, statErr := next.Stat(".")
		if statErr != nil || !os.SameFile(info, opened) {
			next.Close()
			return nil, "", failure("cp_path", "a laptop copy parent changed while opening")
		}
		if closeErr := root.Close(); closeErr != nil {
			next.Close()
			return nil, "", fmt.Errorf("close copy ancestor: %w", closeErr)
		}
		root = next
	}
	return root, leaf, nil
}
