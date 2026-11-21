//go:build linux

package host

import (
	"context"
	"errors"
	"net"
	"os"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"golang.org/x/sys/unix"
)

// The raw child creates this own endpoint. It does not adopt an endpoint or
// directory from a caller's pathname. A relative bind avoids HOME path limits.
type purgeOwnedListenerV3 struct {
	root       *os.Root
	rootRef    cachefs.Reference
	listener   *net.UnixListener
	socket     os.FileInfo
	socketFile *os.File
	closed     bool
	closing    bool
	closeErr   error
}

func newPurgeOwnedListenerV3(ctx context.Context, image *purgeAttemptImageV2) (result *purgeOwnedListenerV3, returned error) {
	return createPurgeOwnedListenerV3(ctx, image, nil)
}

// A failed terminal unlink may replace only this SAME already-closed owner’s
// own endpoint. It creates a new genuine native socket; no saved socket row
// or missing pathname is adopted as an original listener.
func createPurgeOwnedListenerV3(ctx context.Context, image *purgeAttemptImageV2, custody *purgeCodeCustodyV3) (result *purgeOwnedListenerV3, returned error) {
	if image == nil {
		return nil, placementError("the actual purge listener has no protected current image")
	}
	if err := image.Check(ctx); err != nil {
		return nil, err
	}
	owner := &purgeOwnedListenerV3{}
	complete := false
	defer func() {
		if !complete {
			closeErr := owner.Close()
			returned = errors.Join(returned, closeErr)
			if closeErr != nil {
				result = owner
			}
		}
	}()
	var err error
	owner.root, err = os.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	owner.rootRef, err = cachefs.CaptureRoot(owner.root)
	if err != nil {
		return nil, err
	}
	if custody == nil {
		if _, err := cachefs.ValidateFile(owner.root, brand.HostBinary, image.copied, 0o100); err != nil {
			return nil, err
		}
	} else {
		if custody.image != image || custody.closed || custody.transaction == nil || !custody.transaction.closed || custody.endpoint == nil || !custody.endpoint.closed {
			return nil, placementError("the cleanup listener has no SAME completed transaction and original endpoint")
		}
		if err := custody.rootRef.CheckRoot(owner.root); err != nil {
			return nil, err
		}
	}
	owner.listener, err = net.ListenUnix("unix", &net.UnixAddr{Name: "attempt.sock", Net: "unix"})
	if err != nil {
		return nil, err
	}
	owner.listener.SetUnlinkOnClose(false)
	owner.socket, err = owner.root.Lstat("attempt.sock")
	if err != nil || owner.socket.Mode()&os.ModeSocket == 0 {
		return nil, errors.Join(placementError("the raw listener lost its original socket creation"), err)
	}
	if err := owner.root.Chmod("attempt.sock", 0o600); err != nil {
		return nil, err
	}
	owner.socket, err = owner.root.Lstat("attempt.sock")
	if err != nil {
		return nil, err
	}
	owner.socketFile, err = owner.root.OpenFile("attempt.sock", unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	actual, err := owner.socketFile.Stat()
	if err != nil || !os.SameFile(actual, owner.socket) {
		return nil, errors.Join(placementError("the actual purge socket lost its retained native creation"), err)
	}
	if err := owner.Check(); err != nil {
		return nil, err
	}
	complete = true
	return owner, nil
}

func (owner *purgeOwnedListenerV3) Check() error {
	if owner == nil || owner.closed || owner.root == nil || owner.listener == nil || owner.socket == nil {
		return placementError("the actual purge listener is absent")
	}
	if err := owner.rootRef.CheckRoot(owner.root); err != nil {
		return err
	}
	info, err := owner.root.Lstat("attempt.sock")
	if err != nil || !os.SameFile(info, owner.socket) || info.Mode() != os.ModeSocket|0o600 {
		return errors.Join(placementError("the actual purge control socket changed"), err)
	}
	return nil
}

func (owner *purgeOwnedListenerV3) Close() error {
	if owner == nil {
		return nil
	}
	if owner.closed {
		if err := observeOriginalPurgeDescriptorCloseV3([]*os.File{owner.socketFile}, []*os.Root{owner.root}); err != nil {
			return err
		}
		return nil
	}
	owner.closing = true
	if owner.listener != nil {
		if err := owner.listener.Close(); !errors.Is(err, net.ErrClosed) {
			owner.closeErr = errors.Join(owner.closeErr, err)
		}
	}
	if owner.root != nil && owner.socket != nil {
		info, err := owner.root.Lstat("attempt.sock")
		if err == nil && os.SameFile(info, owner.socket) {
			owner.closeErr = errors.Join(owner.closeErr, owner.root.Remove("attempt.sock"))
		} else if errors.Is(err, os.ErrNotExist) {
			if owner.socketFile == nil {
				return placementError("the missing actual purge socket has no original native handle")
			}
			actual, statErr := owner.socketFile.Stat()
			_, _, _, links, known := footprintIdentity(actual)
			if statErr != nil || !known || links != 0 || !os.SameFile(actual, owner.socket) {
				return errors.Join(placementError("the actual purge socket unlink is uncertain"), statErr)
			}
		} else if err != nil {
			return errors.Join(owner.closeErr, err)
		} else if err == nil {
			return errors.Join(owner.closeErr, placementError("preserved an unrelated replacement purge socket"))
		}
	}
	if owner.socketFile != nil {
		owner.closeErr = errors.Join(owner.closeErr, owner.socketFile.Close())
	}
	if owner.root != nil {
		owner.closeErr = errors.Join(owner.closeErr, owner.root.Close())
	}
	owner.closed = true
	return owner.closeErr
}
