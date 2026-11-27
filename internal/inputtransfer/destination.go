package inputtransfer

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/files"
)

func receiveFile(ctx context.Context, reader *bufio.Reader, options ReceiveOptions, index int, file api.InputFile) (err error) {
	return applyFile(ctx, options, file, func(out io.Writer) error {
		return receiveItem(ctx, reader, item{Kind: "file", Index: index, Size: file.Size, SHA256: file.SHA256}, out)
	}, func() error {
		return receiveItem(ctx, reader, item{Kind: "directory", Index: index, SHA256: api.EmptyInputHash}, io.Discard)
	})
}

func applyFile(ctx context.Context, options ReceiveOptions, file api.InputFile, payload func(io.Writer) error, directoryPayload func() error) (err error) {
	parent, relative, leaf, rollback, err := destinationParent(ctx, options, file.Path)
	if err != nil {
		return err
	}
	defer parent.Close()
	defer func() {
		if err != nil {
			rollback()
		}
	}()
	if file.Directory {
		if directoryPayload == nil {
			return protocol("a reviewed include cannot create a directory")
		}
		if err = directoryPayload(); err != nil {
			return err
		}
		return receiveDirectory(ctx, options, parent, relative, leaf, file.Mode)
	}
	if err = options.BeforeCreate(ctx, parent); err != nil {
		return err
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return err
	}
	temporary := ".input-" + hex.EncodeToString(nonce[:])
	out, err := parent.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer out.Close()
	defer func() { _ = parent.Remove(temporary) }()
	if err = payload(out); err != nil {
		return err
	}
	if err = out.Chmod(os.FileMode(file.Mode)); err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	if err = verifyDestination(options, parent, relative, leaf); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	// Rename replaces a leaf alias. It never opens or writes its referent.
	if err = parent.Rename(temporary, leaf); err != nil {
		return err
	}
	dir, err := parent.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func destinationParent(ctx context.Context, options ReceiveOptions, target string) (_ *os.Root, _, _ string, cleanup func(), err error) {
	if protectedTarget(target, options.Protected) {
		return nil, "", "", nil, protocol("copied destination is protected")
	}
	type createdDirectory struct {
		name string
		info os.FileInfo
	}
	var created []createdDirectory
	cleanup = func() {
		for index := len(created) - 1; index >= 0; index-- {
			item := created[index]
			parentName, leaf := path.Dir(item.name), path.Base(item.name)
			parent, err := options.Root.OpenRoot(filepath.FromSlash(parentName))
			if err != nil {
				continue
			}
			current, err := parent.Lstat(leaf)
			if err == nil && current.IsDir() && current.Mode()&os.ModeSymlink == 0 && os.SameFile(current, item.info) &&
				verifyDestination(options, parent, parentName, leaf) == nil {
				_ = parent.Remove(leaf) // only an empty directory, never recursive
			}
			parent.Close()
		}
	}
	defer func() {
		if err != nil {
			cleanup()
		}
	}()
	relative, leaf := ".", path.Base(target)
	fullParent := strings.TrimPrefix(path.Dir(target), "/")
	if fullParent == "" {
		fullParent = "."
	}
	if info, statErr := options.Root.Stat(filepath.FromSlash(fullParent)); statErr == nil && info.IsDir() {
		parent, openErr := options.Root.OpenRoot(filepath.FromSlash(fullParent))
		if openErr == nil {
			pinned, pinErr := parent.Stat(".")
			if pinErr == nil && os.SameFile(info, pinned) {
				if err = verifyDestination(options, parent, fullParent, leaf); err == nil {
					return parent, fullParent, leaf, cleanup, nil
				}
			}
			parent.Close()
		}
		if openErr != nil {
			return nil, "", "", cleanup, openErr
		}
		return nil, "", "", cleanup, protocol("the existing destination parent changed")
	}
	parts := strings.Split(strings.TrimPrefix(path.Dir(target), "/"), "/")
	if path.Dir(target) == "/" {
		parts = nil
	}
	for index := 0; ; index++ {
		if err := ctx.Err(); err != nil {
			return nil, "", "", cleanup, err
		}
		dir, err := files.OpenContainedDirectory(options.Root, relative)
		if err != nil {
			return nil, "", "", cleanup, err
		}
		want, err := dir.Stat()
		dir.Close()
		if err != nil {
			return nil, "", "", cleanup, err
		}
		parent, err := options.Root.OpenRoot(filepath.FromSlash(relative))
		if err != nil {
			return nil, "", "", cleanup, err
		}
		got, err := parent.Stat(".")
		if err != nil || !os.SameFile(want, got) {
			parent.Close()
			return nil, "", "", cleanup, protocol("copied destination parent changed")
		}
		remaining := path.Join(path.Join(parts[index:]...), leaf)
		if err = verifyDestination(options, parent, relative, remaining); err != nil {
			parent.Close()
			return nil, "", "", cleanup, err
		}
		if index == len(parts) {
			return parent, relative, leaf, cleanup, nil
		}
		child := parts[index]
		if _, err = parent.Lstat(child); os.IsNotExist(err) {
			if err = options.BeforeCreate(ctx, parent); err == nil {
				err = parent.Mkdir(child, 0700)
				if err == nil {
					info, statErr := parent.Lstat(child)
					if statErr != nil {
						err = statErr
					} else {
						created = append(created, createdDirectory{name: path.Join(relative, child), info: info})
					}
				}
			}
		}
		parent.Close()
		if err != nil {
			return nil, "", "", cleanup, err
		}
		relative = path.Join(relative, child)
	}
}

func receiveDirectory(ctx context.Context, options ReceiveOptions, parent *os.Root, relative, leaf string, mode uint32) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := verifyDestination(options, parent, relative, leaf); err != nil {
		return err
	}
	info, err := parent.Lstat(leaf)
	created := false
	defer func() {
		if created && err != nil && info != nil {
			current, statErr := parent.Lstat(leaf)
			if statErr == nil && current.IsDir() && current.Mode()&os.ModeSymlink == 0 && os.SameFile(current, info) {
				_ = parent.Remove(leaf)
			}
		}
	}()
	if os.IsNotExist(err) {
		if err = options.BeforeCreate(ctx, parent); err != nil {
			return err
		}
		if err = parent.Mkdir(leaf, 0700); err != nil {
			return err
		}
		created = true
		info, err = parent.Lstat(leaf)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return protocol("an empty directory destination conflicts with a file or link")
	}
	directory, err := files.OpenContainedDirectory(parent, leaf)
	if err != nil {
		return err
	}
	defer directory.Close()
	actual, err := directory.Stat()
	current, currentErr := parent.Lstat(leaf)
	if err != nil || currentErr != nil || !os.SameFile(info, actual) || !os.SameFile(info, current) || current.Mode()&os.ModeSymlink != 0 {
		return protocol("an empty directory destination changed")
	}
	if created {
		if err = directory.Chmod(os.FileMode(mode)); err != nil {
			return err
		}
		if err = directory.Sync(); err != nil {
			return err
		}
	}
	return verifyDestination(options, parent, relative, leaf)
}

// verifyDestination compares the pinned parent with the path under the guest
// root and checks its resolved target against the protected start plan. The
// grant root is the isolated guest filesystem, not a host filesystem root.
func verifyDestination(options ReceiveOptions, parent *os.Root, relative, remaining string) error {
	guestBase, err := filepath.EvalSymlinks(options.Root.Name())
	if err != nil {
		return err
	}
	baseInfo, err := os.Stat(guestBase)
	if err != nil {
		return err
	}
	pinnedBase, err := options.Root.Stat(".")
	if err != nil || !os.SameFile(baseInfo, pinnedBase) {
		return protocol("the guest destination root changed")
	}
	actual, err := parent.Stat(".")
	if err != nil {
		return err
	}
	current, err := options.Root.Stat(filepath.FromSlash(relative))
	if err != nil || !os.SameFile(actual, current) {
		return protocol("the destination parent was replaced")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(guestBase, filepath.FromSlash(relative)))
	if err != nil {
		return err
	}
	resolvedInfo, err := os.Stat(resolved)
	if err != nil || !os.SameFile(actual, resolvedInfo) {
		return protocol("the destination alias changed")
	}
	beneath, err := filepath.Rel(guestBase, resolved)
	if err != nil || beneath == ".." || strings.HasPrefix(beneath, ".."+string(filepath.Separator)) || filepath.IsAbs(beneath) {
		return protocol("a destination alias escaped the guest root")
	}
	canonical := path.Join("/", filepath.ToSlash(beneath), remaining)
	if protectedTarget(canonical, options.Protected) {
		return protocol("a destination alias enters protected runtime, state or installed files")
	}
	return nil
}

// ValidateProtected checks the trusted path list before any helper or stream
// starts. These are guest paths only, never paths of a host state generation.
func ValidateProtected(protected []string) error {
	for _, name := range protected {
		if !path.IsAbs(name) || path.Clean(name) != name || strings.ContainsAny(name, "\\\x00\r\n") || name == "/" {
			return fmt.Errorf("invalid protected guest path. Try: repair the protected start plan")
		}
	}
	return nil
}
