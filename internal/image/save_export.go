package image

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"os"
	"path"
	"slices"

	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/proto"
)

// WriteSavedRoot reads the actual descriptor pinned by the guest runtime.
// It never accepts or opens a root path. Directory handles pin each parent;
// links remain link data, and no special file is opened for content.
func WriteSavedRoot(ctx context.Context, root *os.Root, plan SavedRootPlan, output io.Writer) error {
	if ctx == nil || root == nil || output == nil {
		return savedRootChanged()
	}
	filter, err := newSavedRootFilter(plan)
	if err != nil {
		return err
	}
	budget := &savedRootOutput{output: output}
	writer := tar.NewWriter(budget)
	count := 0
	var walk func(*os.Root, string) error
	walk = func(current *os.Root, prefix string) (returned error) {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := current.Stat(".")
		if err != nil {
			return savedRootChanged()
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return savedRootChanged()
		}
		header.Name, header.Format = prefix, tar.FormatPAX
		directory, err := current.Open(".")
		if err != nil {
			return savedRootChanged()
		}
		defer func() { returned = errors.Join(returned, directory.Close()) }()
		if err := savedFileXattrs(directory, header); err != nil {
			return err
		}
		count++
		if count > MaxEntries {
			return tooLarge("the saved root has more than %d entries", MaxEntries)
		}
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			batch, readErr := directory.ReadDir(128)
			for _, entry := range batch {
				name := path.Join(prefix, entry.Name())
				if !savedAbsolutePath("/" + name) {
					return savedRootChanged()
				}
				if filter.exclude("/"+name) != "" {
					continue
				}
				info, err := current.Lstat(entry.Name())
				if err != nil {
					return savedRootChanged()
				}
				if info.Mode()&os.ModeSocket != 0 || info.Mode()&os.ModeDevice != 0 {
					continue
				}
				if info.IsDir() {
					next, err := current.OpenRoot(entry.Name())
					if err != nil {
						return savedRootChanged()
					}
					actual, err := next.Stat(".")
					if err != nil || !os.SameFile(info, actual) {
						next.Close()
						return savedRootChanged()
					}
					err = walk(next, name)
					if err := errors.Join(err, next.Close()); err != nil {
						return err
					}
					continue
				}
				count++
				if count > MaxEntries {
					return tooLarge("the saved root has more than %d entries", MaxEntries)
				}
				if err := writeSavedEntry(ctx, current, entry.Name(), name, info, writer); err != nil {
					return err
				}
			}
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			if readErr != nil {
				return savedRootChanged()
			}
		}
	}
	if err := walk(root, "."); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return writer.Close()
}

func writeSavedEntry(ctx context.Context, root *os.Root, leaf, name string, info os.FileInfo, writer *tar.Writer) (returned error) {
	link := ""
	if info.Mode()&os.ModeSymlink != 0 {
		var err error
		link, err = root.Readlink(leaf)
		if err != nil {
			return savedRootChanged()
		}
	}
	header, err := tar.FileInfoHeader(info, link)
	if err != nil {
		return savedRootChanged()
	}
	header.Name, header.Format = name, tar.FormatPAX
	if !info.Mode().IsRegular() {
		return writer.WriteHeader(header)
	}
	file, err := files.OpenRegular(root, leaf)
	if err != nil {
		return savedRootChanged()
	}
	defer func() { returned = errors.Join(returned, file.Close()) }()
	actual, err := file.Stat()
	if err != nil || !sameSavedFile(info, actual) {
		return savedRootChanged()
	}
	if err := savedFileXattrs(file, header); err != nil {
		return err
	}
	if err := writer.WriteHeader(header); err != nil {
		return err
	}
	if _, err := io.CopyN(writer, &flatContextReader{ctx, file}, info.Size()); err != nil {
		return err
	}
	after, err := file.Stat()
	if err != nil || !sameSavedFile(actual, after) {
		return savedRootChanged()
	}
	named, err := root.Lstat(leaf)
	if err != nil || !sameSavedFile(after, named) {
		return savedRootChanged()
	}
	return ctx.Err()
}

func sameSavedFile(first, second os.FileInfo) bool {
	return os.SameFile(first, second) && first.Mode() == second.Mode() && first.Size() == second.Size() && first.ModTime().Equal(second.ModTime())
}

type savedRootOutput struct {
	output  io.Writer
	written int64
}

func (output *savedRootOutput) Write(data []byte) (int, error) {
	if int64(len(data)) > MaxUncompressed-output.written {
		return 0, tooLarge("the saved root tar exceeds 16 GiB")
	}
	count, err := output.output.Write(data)
	output.written += int64(count)
	return count, err
}

// ListSavedChanges snapshots bounded upper-overlay paths for advisory review.
// The upper descriptor is pinned before pivot; a guest cannot select it anew.
func ListSavedChanges(ctx context.Context, upper *os.Root, maxEntries int) ([]string, error) {
	if ctx == nil || upper == nil || maxEntries != proto.ImageChangesMax {
		return nil, savedRootChanged()
	}
	var result []string
	bytes := 0
	var walk func(*os.Root, string) error
	walk = func(current *os.Root, prefix string) (returned error) {
		if err := ctx.Err(); err != nil {
			return err
		}
		file, err := current.Open(".")
		if err != nil {
			return savedRootChanged()
		}
		defer func() { returned = errors.Join(returned, file.Close()) }()
		for {
			batch, readErr := file.ReadDir(128)
			for _, entry := range batch {
				if err := ctx.Err(); err != nil {
					return err
				}
				name := path.Join(prefix, entry.Name())
				absolute := "/" + name
				bytes += len(absolute)
				if !savedAbsolutePath(absolute) || len(result) == maxEntries || bytes > proto.ImageChangesBytes {
					return tooLarge("the saved image changes exceed 100000 paths or 8 MiB")
				}
				result = append(result, absolute)
				info, err := current.Lstat(entry.Name())
				if err != nil {
					return savedRootChanged()
				}
				if info.IsDir() {
					next, err := current.OpenRoot(entry.Name())
					if err != nil {
						return savedRootChanged()
					}
					actual, err := next.Stat(".")
					if err != nil || !os.SameFile(info, actual) {
						next.Close()
						return savedRootChanged()
					}
					err = walk(next, name)
					if err := errors.Join(err, next.Close()); err != nil {
						return err
					}
				}
			}
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			if readErr != nil {
				return savedRootChanged()
			}
		}
	}
	if err := walk(upper, ""); err != nil {
		return nil, err
	}
	slices.Sort(result)
	return result, nil
}

func savedRootChanged() error {
	return saveFailure("image_changed", "the original guest root changed or could not be read safely")
}
