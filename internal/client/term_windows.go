//go:build windows

package client

import (
	"os"
	"time"

	"golang.org/x/term"
)

// termSize returns the terminal's size in cells. Windows reports no pixel size.
func termSize(f *os.File) (cols, rows, xpx, ypx int) {
	c, r, err := term.GetSize(int(f.Fd()))
	if err != nil || c == 0 {
		return 80, 24, 0, 0
	}
	return c, r, 0, 0
}

// watchResize polls the console size, because Windows has no SIGWINCH.
func watchResize(f *os.File) (<-chan struct{}, func()) {
	out := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		c0, r0, _, _ := termSize(f)
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if c, r, _, _ := termSize(f); c != c0 || r != r0 {
					c0, r0 = c, r
					select {
					case out <- struct{}{}:
					default:
					}
				}
			case <-done:
				return
			}
		}
	}()
	return out, func() { close(done) }
}

// controlMaster reports whether this OpenSSH can share connections.
// The Windows port of OpenSSH cannot.
const controlMaster = false

// privateDir is never needed on Windows, which shares no connections.
func privateDir(string) bool { return false }

// ownedDir is never needed on Windows either.
func ownedDir(string) bool { return false }

func controlFileOwned(os.FileInfo) bool { return false }

func controlScope() (string, bool) { return "", false }
