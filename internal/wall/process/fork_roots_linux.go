//go:build linux

package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// WithForkRootsV1 borrows only this original launcher's confirmed live root.
// It joins any earlier copy before the callback, and nativeExit joins the
// callback before closing its original descriptors. The caller freezes and
// thaws INSIDE the callback so a pending copy cannot strand a frozen helper.
// The borrowed roots close when the callback returns and must not escape it.
func (owner *CopyOwnerV1) WithForkRootsV1(ctx context.Context, use func(work, home *os.Root) error) (returned error) {
	if owner == nil || owner.native == nil || ctx == nil || use == nil {
		return setupError("fork has no original process file owner", nil)
	}
	native := owner.native
	for !native.mu.TryLock() {
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	defer native.mu.Unlock()
	if err := owner.CheckLiveV1(ctx); err != nil {
		return err
	}
	if native.active != nil || native.publisher >= 0 {
		return setupError("the original process file owner has an unfinished copy", nil)
	}
	mount := native.mounts[copyRootIndex]
	if mount == nil || mount.root == nil {
		return setupError("the original launcher supplied no confirmed live root", nil)
	}
	rootInfo, err := mount.root.Stat()
	if err != nil || !rootInfo.IsDir() {
		return setupError("the original launcher root is unavailable", err)
	}
	work, err := forkRootFromOriginalV1(mount.root, "work")
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, work.Close()) }()
	home, err := forkRootFromOriginalV1(mount.root, "home/agent")
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, home.Close()) }()
	if err := owner.CheckLiveV1(ctx); err != nil {
		return err
	}
	if err := use(work, home); err != nil {
		return err
	}
	return owner.CheckLiveV1(ctx)
}

func forkRootFromOriginalV1(original *os.File, fixed string) (_ *os.Root, returned error) {
	fd, err := unix.Openat2(int(original.Fd()), fixed, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return nil, setupError("cannot pin a live fork source under its original launcher root", err)
	}
	file := os.NewFile(uintptr(fd), "original-fork-root")
	defer func() { returned = errors.Join(returned, file.Close()) }()
	want, err := file.Stat()
	if err != nil || !want.IsDir() {
		return nil, setupError("the original fork source is not a directory", err)
	}
	// This fixed own-fd path wraps the exact descriptor above. It never
	// accepts a caller path or uses a saved manifest to mint a source root.
	root, err := os.OpenRoot(fmt.Sprintf("/proc/self/fd/%d", fd))
	if err != nil {
		return nil, setupError("cannot wrap the original fork descriptor", err)
	}
	check, err := root.Open(".")
	if err != nil {
		return nil, errors.Join(err, root.Close())
	}
	got, statErr := check.Stat()
	closeErr := check.Close()
	if statErr != nil || closeErr != nil || !os.SameFile(want, got) {
		return nil, errors.Join(setupError("the rooted fork descriptor changed", nil), statErr, closeErr, root.Close())
	}
	return root, nil
}
