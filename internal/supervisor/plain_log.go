package supervisor

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
)

// logKeep is the most of the guest holder's log that the test driver
// keeps from one run.
const logKeep = 1 << 20

// keepHolderLog moves the log of the holder of the sandbox rooted at root
// out of the root, to the end of the session's paths.GuestHolderLog, where
// a new start does not remove it. That log turns over at 1 MiB.
//
// The guest wrote the file and the folders above it. So the driver opens
// each folder under the root without following a link, takes only a
// regular file, reads at most its last logKeep bytes, and removes it from
// the folder that it opened.
func keepHolderLog(root, id string) {
	keepHolderLogAt(root, id, guest.FilesFor(root, id).Manifest)
	keepHolderLogAt(root, id, guest.Files(root).Manifest)
}

func keepHolderLogAt(root, id, manifest string) {
	dir := filepath.Dir(manifest)
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return
	}
	const flags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	fd, err := unix.Open(root, flags, 0)
	if err != nil {
		return
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		next, err := unix.Openat(fd, part, flags, 0)
		_ = unix.Close(fd)
		if err != nil {
			return
		}
		fd = next
	}
	defer unix.Close(fd)
	lf, err := unix.Openat(fd, "holder.log", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return
	}
	defer func() { _ = unix.Unlinkat(fd, "holder.log", 0) }()
	f := os.NewFile(uintptr(lf), "holder.log")
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return
	}
	if fi.Size() > logKeep {
		if _, err := f.Seek(fi.Size()-logKeep, io.SeekStart); err != nil {
			return
		}
	}
	w, err := host.OpenLog(paths.GuestHolderLog(id))
	if err != nil {
		return
	}
	defer w.Close()
	_, _ = io.Copy(w, io.LimitReader(f, logKeep))
}

// openInitLog opens the guest init's log, path, for appending, with mode
// 0600, as a file that the guest init writes itself. It turns over to the
// same name with ".1" added first, once it has logKeep bytes or more: the
// guest init holds it open while it runs, so it cannot turn over then.
func openInitLog(path string) (*os.File, error) {
	if st, err := os.Lstat(path); err == nil && st.Size() >= logKeep {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open the guest init's log: %w", err)
	}
	return f, nil
}
