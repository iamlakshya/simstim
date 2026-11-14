package filecopy

import (
	"context"
	"errors"
	"io"
	"os"
	"path"
	"slices"

	"github.com/iamlakshya/simstim/internal/files"
)

// Entry is actual local filesystem metadata, not a wire declaration or copy
// permission. Callers project only Path, Kind and regular Size into their ONE
// versioned protocol record; Info stays local for sealed source checks.
type Entry struct {
	Path, Kind string
	Size       int64
	Info       os.FileInfo
}

func entry(name string, info os.FileInfo) Entry {
	e := Entry{Path: name, Kind: "special", Info: info}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		e.Kind = "symlink"
	case info.IsDir():
		e.Kind = "directory"
	case info.Mode().IsRegular():
		e.Kind = "file"
		e.Size = info.Size()
	}
	return e
}

// List never follows a selected source link or a link below it. It retains at
// most maxEntries metadata records and reads directory entries in small batches.
func List(ctx context.Context, root *os.Root, relative string, maxEntries int) (_ []Entry, isDirectory bool, err error) {
	if root == nil || maxEntries <= 0 || maxEntries > 10000 {
		return nil, false, failure("cp_path", "the copy listing bound is invalid")
	}
	if relative != "." {
		if err := Relative(relative); err != nil {
			return nil, false, err
		}
	}
	parent, err := directory(root, path.Dir(relative), false)
	if err != nil {
		return nil, false, err
	}
	defer func() { err = errors.Join(err, parent.Close()) }()
	leaf := path.Base(relative)
	info, err := parent.Lstat(leaf)
	if err != nil {
		return nil, false, err
	}
	if !info.IsDir() {
		return []Entry{entry(leaf, info)}, false, nil
	}
	selected, err := directory(parent, leaf, false)
	if err != nil {
		return nil, false, err
	}
	defer func() { err = errors.Join(err, selected.Close()) }()
	var result []Entry
	var walk func(*os.Root, string) error
	walk = func(current *os.Root, prefix string) (returned error) {
		file, err := current.Open(".")
		if err != nil {
			return err
		}
		defer func() { returned = errors.Join(returned, file.Close()) }()
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			batch, readErr := file.ReadDir(128)
			for _, item := range batch {
				if len(result) == maxEntries {
					return failure("cp_too_big", "the copy lists more than 10000 entries")
				}
				if err := Name(item.Name()); err != nil {
					return err
				}
				info, err := current.Lstat(item.Name())
				if err != nil {
					return err
				}
				name := path.Join(prefix, item.Name())
				result = append(result, entry(name, info))
				if info.IsDir() {
					next, err := directory(current, item.Name(), false)
					if err != nil {
						return err
					}
					err = walk(next, name)
					err = errors.Join(err, next.Close())
					if err != nil {
						return err
					}
				}
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
			if readErr != nil {
				return readErr
			}
		}
		return nil
	}
	if err := walk(selected, ""); err != nil {
		return nil, false, err
	}
	slices.SortFunc(result, func(a, b Entry) int {
		if a.Path < b.Path {
			return -1
		}
		if a.Path > b.Path {
			return 1
		}
		return 0
	})
	return result, true, nil
}

// WriteChunk reads only an ordinary descriptor beneath the selected top root.
// It emits at most the requested bytes. The fixed-account caller owns source
// permission and the versioned get completion/error record.
func WriteChunk(ctx context.Context, root *os.Root, relative string, offset, maxBytes int64, output io.Writer) (count int64, err error) {
	if root == nil || output == nil || offset < 0 || maxBytes <= 0 || maxBytes > 1<<20 {
		return 0, failure("cp_path", "the file read bound is invalid")
	}
	if err := Relative(relative); err != nil {
		return 0, err
	}
	parent, err := directory(root, path.Dir(relative), false)
	if err != nil {
		return 0, err
	}
	defer func() { err = errors.Join(err, parent.Close()) }()
	file, err := files.OpenRegular(parent, path.Base(relative))
	if err != nil {
		return 0, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	if _, err = file.Seek(offset, io.SeekStart); err != nil {
		return 0, err
	}
	return io.Copy(output, io.LimitReader(contextReader{ctx, file}, maxBytes))
}
