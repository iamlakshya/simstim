package supervisor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"strings"

	"github.com/iamlakshya/simstim/internal/files"
)

// copyForkProcessV1 receives only roots borrowed from the actual original
// launcher/copy owner while all guest writers are cgroup frozen. File-plan
// names select DATA below that owned home; they never select a host root.
func copyForkProcessV1(ctx context.Context, work, home, destination *os.Root, plan files.StatePlan) error {
	if ctx == nil || work == nil || home == nil || destination == nil {
		return execRefusal("the process fork has no original native file roots")
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	if err := destination.Mkdir("work", 0700); err != nil {
		return err
	}
	childWork, err := destination.OpenRoot("work")
	if err != nil {
		return err
	}
	err = errors.Join(copyForkTreeV1(ctx, work, childWork), childWork.Close())
	if err != nil {
		return err
	}
	if err := destination.Mkdir("state", 0700); err != nil {
		return err
	}
	state, err := destination.OpenRoot("state")
	if err != nil {
		return err
	}
	defer state.Close()
	for _, name := range plan.Dirs {
		source, err := openForkRootV1(home, name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if err := state.MkdirAll(name, 0700); err != nil {
			source.Close()
			return err
		}
		child, err := state.OpenRoot(name)
		if err != nil {
			source.Close()
			return err
		}
		err = errors.Join(copyForkTreeV1(ctx, source, child), source.Close(), child.Close())
		if err != nil {
			return err
		}
	}
	if len(plan.Files) > 0 {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return err
		}
		epoch := "e-" + hex.EncodeToString(random[:])
		if err := destination.MkdirAll("state-files/epochs/"+epoch, 0700); err != nil {
			return err
		}
		committed, err := destination.OpenRoot("state-files/epochs/" + epoch)
		if err != nil {
			return err
		}
		for _, name := range plan.Files {
			main, err := files.OpenRegular(home, name)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				committed.Close()
				return err
			}
			if err := main.Close(); err != nil {
				committed.Close()
				return err
			}
			for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
				err := copyForkFileV1(ctx, home, committed, name+suffix)
				if suffix != "" && errors.Is(err, os.ErrNotExist) {
					continue
				}
				if err != nil {
					committed.Close()
					return err
				}
			}
		}
		if err := committed.Close(); err != nil {
			return err
		}
		current, err := destination.OpenFile("state-files/CURRENT", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, writeErr := io.WriteString(current, epoch+"\n")
		if err := errors.Join(writeErr, current.Sync(), current.Close()); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func openForkRootV1(parent *os.Root, name string) (_ *os.Root, returned error) {
	if !files.Relative(name) {
		return nil, execRefusal("the fork source directory has an invalid relative name")
	}
	current, err := parent.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(name, "/") {
		before, err := current.Lstat(part)
		if err != nil {
			current.Close()
			return nil, err
		}
		if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
			current.Close()
			return nil, execRefusal("a fork source directory is a symbolic link or special file")
		}
		next, err := current.OpenRoot(part)
		if err != nil {
			current.Close()
			return nil, err
		}
		after, err := next.Stat(".")
		if err != nil || !os.SameFile(before, after) {
			next.Close()
			current.Close()
			return nil, errors.Join(execRefusal("the fork source directory changed while opening"), err)
		}
		if err := current.Close(); err != nil {
			next.Close()
			return nil, err
		}
		current = next
	}
	return current, nil
}

func copyForkTreeV1(ctx context.Context, source, destination *os.Root) (returned error) {
	reader, err := source.Open(".")
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, reader.Close()) }()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, readErr := reader.ReadDir(128)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			name := entry.Name()
			info, err := source.Lstat(name)
			if err != nil {
				return err
			}
			switch {
			case info.IsDir():
				original, err := openForkRootV1(source, name)
				if err != nil {
					return err
				}
				if err := destination.Mkdir(name, 0700); err != nil {
					original.Close()
					return err
				}
				child, err := destination.OpenRoot(name)
				if err != nil {
					original.Close()
					return err
				}
				err = errors.Join(copyForkTreeV1(ctx, original, child), original.Close(), child.Close())
				if err != nil {
					return err
				}
				if err := destination.Chmod(name, info.Mode().Perm()); err != nil {
					return err
				}
			case info.Mode().IsRegular():
				if err := copyForkFileV1(ctx, source, destination, name); err != nil {
					return err
				}
			case info.Mode()&os.ModeSymlink != 0:
				link, err := source.Readlink(name)
				if err != nil {
					return err
				}
				if err := destination.Symlink(link, name); err != nil {
					return err
				}
			default:
				return execRefusal("a fork source contains a socket, device or other special file")
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func copyForkFileV1(ctx context.Context, source, destination *os.Root, name string) (returned error) {
	file, err := files.OpenRegular(source, name)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, file.Close()) }()
	before, err := file.Stat()
	if err != nil {
		return err
	}
	if err := destination.MkdirAll(path.Dir(name), 0700); err != nil {
		return err
	}
	output, err := destination.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		returned = errors.Join(returned, output.Close())
		if !complete {
			returned = errors.Join(returned, destination.Remove(name))
		}
	}()
	cloned, err := cloneForkFileV1(output, file)
	if err != nil {
		return err
	}
	if !cloned {
		buffer := make([]byte, 1<<20)
		for offset := int64(0); offset < before.Size(); {
			if err := ctx.Err(); err != nil {
				return err
			}
			block := buffer[:min(int64(len(buffer)), before.Size()-offset)]
			n, err := file.ReadAt(block, offset)
			if n != len(block) || err != nil && !errors.Is(err, io.EOF) {
				return errors.Join(err, io.ErrUnexpectedEOF)
			}
			zero := true
			for _, value := range block {
				if value != 0 {
					zero = false
					break
				}
			}
			if !zero {
				n, err := output.WriteAt(block, offset)
				if n != len(block) || err != nil {
					return errors.Join(err, io.ErrShortWrite)
				}
			}
			offset += int64(len(block))
		}
		if err := output.Truncate(before.Size()); err != nil {
			return err
		}
	}
	after, err := file.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return execRefusal("a frozen fork source file changed during its copy")
	}
	if err := errors.Join(ctx.Err(), output.Chmod(before.Mode().Perm()), output.Sync()); err != nil {
		return err
	}
	complete = true
	return nil
}
