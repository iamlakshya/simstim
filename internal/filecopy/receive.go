package filecopy

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"strings"
)

// directory opens each directory relative to the held root. It rejects every
// symlink before opening, including links that would stay inside os.Root.
func directory(root *os.Root, name string, create bool) (*os.Root, error) {
	current, err := root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	if name == "." || name == "" {
		return current, nil
	}
	for _, part := range strings.Split(name, "/") {
		if err := Name(part); err != nil {
			current.Close()
			return nil, err
		}
		info, err := current.Lstat(part)
		if errors.Is(err, os.ErrNotExist) && create {
			if err = current.Mkdir(part, 0755); err != nil && !errors.Is(err, os.ErrExist) {
				current.Close()
				return nil, err
			}
			info, err = current.Lstat(part)
		}
		if err != nil {
			current.Close()
			return nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			current.Close()
			return nil, failure("cp_path", "a copy parent is a link or is not a directory")
		}
		next, err := current.OpenRoot(part)
		if err != nil {
			current.Close()
			return nil, err
		}
		opened, statErr := next.Stat(".")
		if statErr != nil || !os.SameFile(info, opened) {
			next.Close()
			current.Close()
			return nil, failure("cp_path", "a copy parent changed while opening")
		}
		if err := current.Close(); err != nil {
			next.Close()
			return nil, err
		}
		current = next
	}
	return current, nil
}

func temporary(root *os.Root) (*os.File, string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, "", err
	}
	name := ".cp-" + hex.EncodeToString(random[:])
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	return file, name, err
}

type contextReader struct {
	ctx   context.Context
	input io.Reader
}

func (r contextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.input.Read(data)
}

// ReceiveFile is the actual fixed-account put primitive. The caller has
// already selected a permitted top-level root and command-wide size cap. The
// exact stream must end after size bytes. No guest mode, owner or time crosses.
func ReceiveFile(ctx context.Context, root *os.Root, relative string, size int64, digest string, replace bool, input io.Reader) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if root == nil || input == nil || size < 0 || len(digest) != 64 {
		return failure("cp_path", "the copy declaration is incomplete")
	}
	if _, decodeErr := hex.DecodeString(digest); decodeErr != nil {
		return failure("cp_path", "the copy digest is invalid")
	}
	if err := Relative(relative); err != nil {
		return err
	}
	parent, err := directory(root, path.Dir(relative), true)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, parent.Close()) }()
	leaf := path.Base(relative)
	if info, statErr := parent.Lstat(leaf); statErr == nil {
		if !replace || info.IsDir() {
			return failure("cp_exists", "the copy destination already exists")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	file, temp, err := temporary(parent)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			err = errors.Join(err, file.Close())
		}
		if removeErr := parent.Remove(temp); !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, removeErr)
		}
	}()
	hash := sha256.New()
	n, err := io.CopyN(io.MultiWriter(file, hash), contextReader{ctx, input}, size)
	if err != nil || n != size {
		return errors.Join(failure("cp_too_big", "the copied file differs from its listed size"), err)
	}
	var extra [1]byte
	more, endErr := (contextReader{ctx, input}).Read(extra[:])
	if more != 0 || !errors.Is(endErr, io.EOF) {
		return errors.Join(failure("cp_too_big", "the copied file sends bytes beyond its listed size"), endErr)
	}
	if hex.EncodeToString(hash.Sum(nil)) != strings.ToLower(digest) {
		return failure("cp_changed", "the copied file differs from its checked digest")
	}
	if err = file.Chmod(0644); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	err = file.Close()
	closed = true
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if replace {
		return parent.Rename(temp, leaf)
	}
	// Link is an atomic create-if-absent. A file planted after preflight is
	// never replaced, and a planted final symlink is never followed.
	if err = parent.Link(temp, leaf); errors.Is(err, os.ErrExist) {
		return failure("cp_exists", "a copy destination appeared before publication")
	}
	return err
}
