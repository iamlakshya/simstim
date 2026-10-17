package diag

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The limits of the debug folders (decision 0025). A folder over its limit
// loses its oldest files first, at each start and each save.
const (
	// FolderMax is the limit of a debug folder, on the laptop and on a host.
	FolderMax = 64 << 20
	// SessionFolderMax is the limit of a session's debug folder on a host.
	SessionFolderMax = 32 << 20
)

// OutsideList is the file in the laptop's debug folder that lists the
// debug files outside it, one path a line, for the bundle.
const OutsideList = "outside.txt"

// TimeName is the local time as files name it, such as 20261016-185712.
func TimeName(t time.Time) string { return t.Format("20060102-150405") }

// folderFile is one file of a debug folder.
type folderFile struct {
	path string
	size int64
	mod  time.Time
	auto bool // a window that a process saved by itself
}

// listFolder lists the regular files in dir, oldest first, and their total
// size. It leaves out OutsideList, which no turnover removes, but counts
// its size.
func listFolder(dir string) (files []folderFile, total int64) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0
	}
	for _, e := range entries {
		fi, err := e.Info()
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		total += fi.Size()
		if e.Name() != OutsideList {
			files = append(files, folderFile{filepath.Join(dir, e.Name()), fi.Size(), fi.ModTime(), strings.Contains(e.Name(), AutoMark+"flight-")})
		}
	}
	sort.SliceStable(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	return files, total
}

// remove removes a file unless a File has it open or a writer holds it. A
// file that another turnover removed first counts as removed too.
func (f folderFile) remove() bool {
	if inUse(f.path) {
		return false
	}
	err := os.Remove(f.path)
	return err == nil || errors.Is(err, fs.ErrNotExist)
}

// Prune removes the oldest files in dir until the files there hold max
// bytes or less: the windows that processes saved by themselves first,
// then the rest. It never removes a file in use, nor OutsideList. It
// reports how many files it removed. A max of 0 sets no limit.
func Prune(dir string, max int64) int {
	if max <= 0 {
		return 0
	}
	files, total := listFolder(dir)
	// The automatic saves go first, oldest first. The sort is stable.
	sort.SliceStable(files, func(i, j int) bool { return files[i].auto && !files[j].auto })
	removed := 0
	for _, f := range files {
		if total <= max {
			break
		}
		if f.remove() {
			total -= f.size
			removed++
		}
	}
	return removed
}

// PruneAuto removes the windows that processes saved by themselves in dir,
// all but the newest keep. It removes no other file, so an automatic save
// never pushes out a file made on request.
func PruneAuto(dir string, keep int) int {
	files, _ := listFolder(dir)
	var auto []folderFile
	for _, f := range files {
		if f.auto {
			auto = append(auto, f)
		}
	}
	removed := 0
	for _, f := range auto[:max(len(auto)-keep, 0)] {
		if f.remove() {
			removed++
		}
	}
	return removed
}

// CreatePrivate creates path, or empties it, with mode 0600, in a folder
// with mode 0700. Until the file is closed, the turnover of its folder
// leaves it alone.
func CreatePrivate(path string) (*os.File, error) {
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	for try := 0; ; try++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return nil, fmt.Errorf("create a debug file: %w", err)
		}
		markInUse(f)
		// A turnover between the open and the lock may have removed the new
		// file: then the path names another file, or none. Open it again.
		if try >= 3 || samePath(f, path) {
			return f, nil
		}
		f.Close()
	}
}

// samePath reports whether path still names the open file f.
func samePath(f *os.File, path string) bool {
	open, err := f.Stat()
	if err != nil {
		return false
	}
	now, err := os.Stat(path)
	return err == nil && os.SameFile(open, now)
}

// EnsureDir makes dir, and its parents, with mode 0700.
func EnsureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("make the debug folder: %w", err)
	}
	//nolint:gosec // G302: a folder needs the execute bit to be opened, and 0700 is the owner's alone
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("make the debug folder private: %w", err)
	}
	return nil
}

// WriteFile writes data to path, as CreatePrivate makes it.
func WriteFile(path string, data []byte) error {
	f, err := CreatePrivate(path)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	return nil
}
