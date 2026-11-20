//go:build !windows

package client

import (
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/sys/unix"
)

// termSize returns the terminal's size in cells and pixels.
func termSize(f *os.File) (cols, rows, xpx, ypx int) {
	ws, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Col == 0 {
		return 80, 24, 0, 0
	}
	return int(ws.Col), int(ws.Row), int(ws.Xpixel), int(ws.Ypixel)
}

// watchResize delivers a value on the returned channel when the terminal is resized.
func watchResize(_ *os.File) (<-chan struct{}, func()) {
	sig := make(chan os.Signal, 1)
	out := make(chan struct{}, 1)
	signal.Notify(sig, syscall.SIGWINCH)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-sig:
				select {
				case out <- struct{}{}:
				default:
				}
			case <-done:
				return
			}
		}
	}()
	return out, func() { signal.Stop(sig); close(done) }
}

// controlMaster reports whether this OpenSSH can share connections.
const controlMaster = true

// ownerOf is the id of the user who owns fi, or -1. Tests fake it, as if a
// folder were another user's.
var ownerOf = func(fi os.FileInfo) int {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid)
	}
	return -1
}

// privateDir reports whether d is a real folder (not a link) that only this
// user owns and can open, so nobody else can plant or read sockets in it.
func privateDir(d string) bool {
	fi, err := os.Lstat(d)
	if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		return false
	}
	return ownerOf(fi) == os.Getuid()
}

// ownedDir reports whether d is a real folder (not a link) that this user
// owns, and that neither its group nor others may write: nobody else can
// move or replace a folder in it.
func ownedDir(d string) bool {
	fi, err := os.Lstat(d)
	if err != nil || !fi.IsDir() || fi.Mode().Perm()&0o022 != 0 {
		return false
	}
	return ownerOf(fi) == os.Getuid()
}
