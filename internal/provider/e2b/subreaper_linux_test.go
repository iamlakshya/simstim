//go:build linux

package e2b

import (
	"errors"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// ownEscaped makes this process the subreaper of its descendants for the rest
// of the test, so a grandchild whose wrapper has exited becomes our own child
// instead of init's. wait reaps that child once the fake's own wrappers have
// been reaped, and is safe to call on every exit path.
func ownEscaped(t *testing.T) func(mustReap bool) {
	t.Helper()
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		t.Fatalf("set child subreaper: %v", err)
	}
	t.Cleanup(func() {
		_ = unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 0, 0, 0, 0)
	})
	var once sync.Once
	return func(mustReap bool) {
		once.Do(func() {
			deadline := time.Now().Add(30 * time.Second)
			for {
				var ws unix.WaitStatus
				pid, err := unix.Wait4(-1, &ws, unix.WNOHANG, nil)
				if pid > 0 {
					return
				}
				if errors.Is(err, syscall.ECHILD) {
					if mustReap {
						t.Error("the escaped child was not owned and reaped by the test")
					}
					return
				}
				if err != nil && !errors.Is(err, syscall.EINTR) {
					t.Errorf("wait for the escaped child: %v", err)
					return
				}
				if time.Now().After(deadline) {
					t.Error("the escaped child was not reaped in 30s")
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
		})
	}
}
