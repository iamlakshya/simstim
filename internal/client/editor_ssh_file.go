package client

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/cachefs"
)

type editorSSHFileV1 struct {
	directory string
	root      *os.Root
	rootRef   cachefs.Reference
	name      string
	lock      *os.File
	lockRef   cachefs.Reference
	file      *os.File
	fileRef   cachefs.Reference
	mode      os.FileMode
	contents  []byte
	absent    bool
}

func editorSSHFileErrorV1(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return &editorSSHFileFailureV1{user: &UserError{Msg: "the owned editor SSH configuration could not be changed: " + err.Error(), Hint: "Check the SSH folder's ownership and mode 0700, then retry " + brand.Name + " ssh-config.", Code: "ssh_config_file", Kind: KindRefused}, cause: err}
}

type editorSSHFileFailureV1 struct {
	user  *UserError
	cause error
}

func (e *editorSSHFileFailureV1) Error() string   { return e.user.Error() }
func (e *editorSSHFileFailureV1) Unwrap() []error { return []error{e.user, e.cause} }

func openEditorSSHFileV1(ctx context.Context, directory, name string, create bool) (_ *editorSSHFileV1, returned error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := cachefs.OpenPrivateRoot(directory, create)
	if err != nil {
		return nil, editorSSHFileErrorV1(err)
	}
	owned := &editorSSHFileV1{directory: directory, root: root, name: name, mode: 0o600}
	keep := false
	defer func() {
		if !keep {
			_ = owned.close()
		}
	}()
	owned.rootRef, err = cachefs.CaptureRoot(root)
	if err != nil {
		return nil, editorSSHFileErrorV1(err)
	}
	owned.lock, err = cachefs.OpenFile(root, name+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, editorSSHFileErrorV1(err)
	}
	owned.lockRef, err = cachefs.CaptureFile(root, name+".lock", owned.lock, 0o600)
	if err != nil {
		return nil, editorSSHFileErrorV1(err)
	}
	wait, cancel := context.WithTimeout(ctx, hostsWait)
	defer cancel()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		locked, err := cachefs.TryLock(owned.lock, cachefs.Exclusive)
		if err != nil {
			return nil, editorSSHFileErrorV1(err)
		}
		if locked {
			break
		}
		select {
		case <-wait.Done():
			return nil, editorSSHFileErrorV1(wait.Err())
		case <-tick.C:
		}
	}
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		owned.absent = true
	} else if err != nil {
		return nil, editorSSHFileErrorV1(err)
	} else {
		owned.mode = info.Mode().Perm()
		if owned.mode != 0o600 && owned.mode != 0o644 {
			return nil, editorSSHFileErrorV1(fmt.Errorf("SSH configuration has an unsafe file mode"))
		}
		owned.file, err = cachefs.OpenFile(root, name, os.O_RDONLY, owned.mode)
		if err != nil {
			return nil, editorSSHFileErrorV1(err)
		}
		owned.fileRef, err = cachefs.CaptureFile(root, name, owned.file, owned.mode)
		if err != nil {
			return nil, editorSSHFileErrorV1(err)
		}
		if owned.fileRef.Size > 1<<20 {
			return nil, editorSSHFileErrorV1(fmt.Errorf("SSH configuration exceeds 1 MiB"))
		}
		owned.contents, err = io.ReadAll(io.LimitReader(owned.file, 1<<20+1))
		if err != nil || len(owned.contents) > 1<<20 {
			return nil, editorSSHFileErrorV1(fmt.Errorf("SSH configuration cannot be read within its bound"))
		}
	}
	if err := owned.check(ctx); err != nil {
		return nil, err
	}
	keep = true
	return owned, nil
}

func (f *editorSSHFileV1) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if f == nil || f.root == nil || f.lock == nil {
		return editorSSHFileErrorV1(fmt.Errorf("SSH writer has no owned native source"))
	}
	if err := f.rootRef.CheckRoot(f.root); err != nil {
		return editorSSHFileErrorV1(err)
	}
	current, err := cachefs.OpenPrivateRoot(f.directory, false)
	if err != nil {
		return editorSSHFileErrorV1(err)
	}
	matched := f.rootRef.CheckRoot(current)
	closed := current.Close()
	if err := errors.Join(matched, closed); err != nil {
		return editorSSHFileErrorV1(err)
	}
	if err := f.lockRef.CheckCreatedFile(f.root, f.name+".lock", f.lock, 0o600); err != nil {
		return editorSSHFileErrorV1(err)
	}
	if f.absent {
		if _, err := f.root.Lstat(f.name); !errors.Is(err, os.ErrNotExist) {
			return editorSSHFileErrorV1(fmt.Errorf("an absent SSH file appeared during the operation"))
		}
		return nil
	}
	return editorSSHFileErrorV1(f.fileRef.CheckFile(f.root, f.name, f.file, f.mode))
}

func (f *editorSSHFileV1) replace(ctx context.Context, data []byte) (returned error) {
	if len(data) > 1<<20 {
		return editorSSHFileErrorV1(fmt.Errorf("SSH configuration exceeds 1 MiB"))
	}
	if err := f.check(ctx); err != nil {
		return err
	}
	name := ".editor-ssh-" + rand.Text()
	part, err := f.root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return editorSSHFileErrorV1(err)
	}
	defer func() { returned = errors.Join(returned, part.Close()) }()
	created, err := cachefs.CaptureFile(f.root, name, part, 0o600)
	if err != nil {
		return editorSSHFileErrorV1(err)
	}
	published := false
	partMode := os.FileMode(0o600)
	defer func() {
		if !published && created.CheckCreatedFile(f.root, name, part, partMode) == nil {
			_ = f.root.Remove(name)
		}
	}()
	if _, err := part.Write(data); err != nil {
		return editorSSHFileErrorV1(err)
	}
	if err := part.Chmod(f.mode); err != nil {
		return editorSSHFileErrorV1(err)
	}
	partMode = f.mode
	if err := part.Sync(); err != nil {
		return editorSSHFileErrorV1(err)
	}
	after, err := cachefs.CaptureFile(f.root, name, part, f.mode)
	if err != nil {
		return editorSSHFileErrorV1(err)
	}
	if err := f.check(ctx); err != nil {
		return err
	}
	if err := after.CheckFile(f.root, name, part, f.mode); err != nil {
		return editorSSHFileErrorV1(err)
	}
	if err := f.root.Rename(name, f.name); err != nil {
		return editorSSHFileErrorV1(err)
	}
	published = true
	if err := syncEditorSSHDirectoryV1(f.root); err != nil {
		return editorSSHFileErrorV1(err)
	}
	if err := after.CheckFile(f.root, f.name, part, f.mode); err != nil {
		return editorSSHFileErrorV1(err)
	}
	if f.file != nil {
		if err := f.file.Close(); err != nil {
			return editorSSHFileErrorV1(err)
		}
	}
	f.file, err = cachefs.OpenFile(f.root, f.name, os.O_RDONLY, f.mode)
	if err != nil {
		return editorSSHFileErrorV1(err)
	}
	f.fileRef, f.absent, f.contents = after, false, append([]byte(nil), data...)
	return f.check(ctx)
}

func syncEditorSSHDirectoryV1(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(cachefs.SyncDirectory(directory), directory.Close())
}

func (f *editorSSHFileV1) remove(ctx context.Context) error {
	if err := f.check(ctx); err != nil {
		return err
	}
	if f.absent {
		return nil
	}
	if err := f.root.Remove(f.name); err != nil {
		return editorSSHFileErrorV1(err)
	}
	f.absent = true
	return editorSSHFileErrorV1(syncEditorSSHDirectoryV1(f.root))
}

func (f *editorSSHFileV1) close() error {
	if f == nil {
		return nil
	}
	var err error
	if f.file != nil {
		err = errors.Join(err, f.file.Close())
		f.file = nil
	}
	if f.lock != nil {
		err = errors.Join(err, f.lock.Close())
		f.lock = nil
	}
	if f.root != nil {
		err = errors.Join(err, f.root.Close())
		f.root = nil
	}
	clear(f.contents)
	f.contents = nil
	return err
}
