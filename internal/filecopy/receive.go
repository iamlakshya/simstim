package filecopy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"strings"
)

// OpenDirectoryRoot opens each directory relative to the held root. It rejects every
// symlink before opening, including links that would stay inside os.Root.
func OpenDirectoryRoot(root *os.Root, name string, create bool) (*os.Root, error) {
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
	file, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
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
	parent, err := OpenDirectoryRoot(root, path.Dir(relative), true)
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
	file, publish, cleanup, err := newStage(parent)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, cleanup()) }()
	if err=ReceivePreparedStage(ctx,file,size,digest,input);err!=nil{return err}
	// The stage owns its live descriptor until cleanup. In the Linux guest,
	// publication links that exact anonymous inode; no helper-owned name is
	// exposed while bytes arrive or left behind when the helper dies.
	return publish(leaf, replace)
}
