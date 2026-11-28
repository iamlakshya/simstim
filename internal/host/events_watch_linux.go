//go:build linux

package host

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

func lockEventsFile(lockPath string) func() {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return func() {}
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		_ = f.Close()
		return func() {}
	}
	return func() {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}
}

func fileInode(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && st != nil {
		return st.Ino
	}
	return 0
}

// watchEventsFile watches logPath's directory via Linux inotify so newly
// appended events wake onChange within milliseconds with zero polling
// (isolation spec 12, 17.6).
func watchEventsFile(ctx context.Context, logPath string, onChange func() error) error {
	dir := filepath.Dir(logPath)
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return fmt.Errorf("init inotify: %w", err)
	}
	defer func() { _ = unix.Close(fd) }()
	if fd < 0 || fd > math.MaxInt32 {
		return fmt.Errorf("inotify fd out of range: %d", fd)
	}

	mask := uint32(unix.IN_MODIFY | unix.IN_CREATE | unix.IN_MOVED_TO | unix.IN_CLOSE_WRITE)
	if _, err := unix.InotifyAddWatch(fd, dir, mask); err != nil {
		return fmt.Errorf("watch events dir: %w", err)
	}
	// Drain any events appended between opening the file and registering the watch.
	if err := onChange(); err != nil {
		return err
	}

	buf := make([]byte, 4096)
	pfds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("watch events stopped: %w", err)
		}
		n, err := unix.Poll(pfds, 250)
		if err != nil {
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			return fmt.Errorf("poll inotify: %w", err)
		}
		if n == 0 {
			continue
		}
		// Drain all queued inotify descriptors before reading new log lines.
		for {
			_, rerr := unix.Read(fd, buf)
			if rerr != nil {
				break
			}
		}
		if err := onChange(); err != nil {
			return err
		}
	}
}
