//go:build linux

package host

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
	"golang.org/x/sys/unix"
)

// This endpoint owns only its original listener creation and anchored socket
// leaf. A connected process still needs current SCM/code/creation checks.
type purgePrivateEndpointV2 struct {
	root       *os.Root
	directory  *os.File
	reference  cachefs.Reference
	listener   *net.UnixListener
	socket     os.FileInfo
	socketFile *os.File
	closed     bool
	closeErr   error
	leaf       string
	path       string
}

func newPurgePrivateEndpointV2() (_ *purgePrivateEndpointV2, returned error) {
	nonce, err := footprintNonce()
	if err != nil {
		return nil, err
	}
	endpoint := &purgePrivateEndpointV2{leaf: ".pg-" + nonce[:16]}
	complete := false
	defer func() {
		if !complete {
			returned = errors.Join(returned, endpoint.Close())
		}
	}()
	endpoint.root, err = cachefs.OpenPrivateRoot(paths.RuntimeDir(), false)
	if err != nil {
		return nil, err
	}
	endpoint.reference, err = cachefs.CaptureRoot(endpoint.root)
	if err != nil {
		return nil, err
	}
	endpoint.directory, err = endpoint.root.Open(".")
	if err != nil {
		return nil, err
	}
	endpoint.path = filepath.Join(endpoint.root.Name(), endpoint.leaf)
	if len(endpoint.path) > 100 {
		return nil, sessionLeaseError("the original private purge control path exceeds its native bound")
	}
	// Bind through the retained original directory. The child connects using
	// the checked named view, so SO_PEERCRED records that actual new child.
	address := fmt.Sprintf("/proc/self/fd/%d/%s", endpoint.directory.Fd(), endpoint.leaf)
	endpoint.listener, err = net.ListenUnix("unix", &net.UnixAddr{Name: address, Net: "unix"})
	if err != nil {
		return nil, err
	}
	endpoint.listener.SetUnlinkOnClose(false)
	endpoint.socket, err = endpoint.root.Lstat(endpoint.leaf)
	if err != nil || endpoint.socket.Mode()&os.ModeSocket == 0 {
		return nil, errors.Join(sessionLeaseError("the new purge endpoint lost its original socket creation"), err)
	}
	if err := endpoint.root.Chmod(endpoint.leaf, 0o600); err != nil {
		return nil, err
	}
	endpoint.socket, err = endpoint.root.Lstat(endpoint.leaf)
	if err != nil || endpoint.socket.Mode() != os.ModeSocket|0o600 {
		return nil, errors.Join(sessionLeaseError("the actual purge control socket has another native role"), err)
	}
	endpoint.socketFile, err = endpoint.root.OpenFile(endpoint.leaf, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	actual, err := endpoint.socketFile.Stat()
	if err != nil || !os.SameFile(actual, endpoint.socket) {
		return nil, errors.Join(sessionLeaseError("the private purge socket lost its retained native creation"), err)
	}
	if err := endpoint.Check(); err != nil {
		return nil, err
	}
	complete = true
	return endpoint, nil
}

func (endpoint *purgePrivateEndpointV2) Check() error {
	if endpoint == nil || endpoint.root == nil || endpoint.listener == nil || endpoint.socket == nil {
		return sessionLeaseError("the original purge endpoint is absent")
	}
	if err := endpoint.reference.CheckRoot(endpoint.root); err != nil {
		return err
	}
	fresh, err := cachefs.OpenPrivateRoot(paths.RuntimeDir(), false)
	if err != nil {
		return err
	}
	if err := errors.Join(endpoint.reference.CheckRoot(fresh), fresh.Close()); err != nil {
		return err
	}
	current, err := endpoint.root.Lstat(endpoint.leaf)
	if err != nil || !os.SameFile(current, endpoint.socket) {
		return errors.Join(sessionLeaseError("the original purge control socket was replaced"), err)
	}
	return nil
}

func (endpoint *purgePrivateEndpointV2) Close() error {
	if endpoint == nil {
		return nil
	}
	if endpoint.closed {
		if err := observeOriginalPurgeDescriptorCloseV3([]*os.File{endpoint.socketFile, endpoint.directory}, []*os.Root{endpoint.root}); err != nil {
			return err
		}
		return nil
	}
	if endpoint.listener != nil {
		if err := endpoint.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			return err
		}
	}
	if endpoint.root != nil && endpoint.socket != nil {
		if endpoint.socketFile == nil {
			return sessionLeaseError("the original private purge socket has no retained native creation")
		}
		actual, err := endpoint.socketFile.Stat()
		_, _, _, links, known := footprintIdentity(actual)
		if err != nil || !known || !os.SameFile(actual, endpoint.socket) {
			return errors.Join(sessionLeaseError("the original private purge socket lost its native identity"), err)
		}
		current, err := endpoint.root.Lstat(endpoint.leaf)
		if err == nil && os.SameFile(current, endpoint.socket) {
			if err := endpoint.root.Remove(endpoint.leaf); err != nil {
				return err
			}
		} else if errors.Is(err, os.ErrNotExist) && links == 0 {
			// SAME original socket descriptor proves the earlier real unlink.
		} else if err != nil {
			return errors.Join(sessionLeaseError("the original private purge socket unlink remains unknown"), err)
		} else if err == nil {
			return sessionLeaseError("preserved a replaced private purge control socket")
		}
		if err := syncCapturedDirectory(endpoint.root); err != nil {
			return err
		}
	}
	if endpoint.socketFile != nil {
		endpoint.closeErr = errors.Join(endpoint.closeErr, endpoint.socketFile.Close())
	}
	if endpoint.directory != nil {
		endpoint.closeErr = errors.Join(endpoint.closeErr, endpoint.directory.Close())
	}
	if endpoint.root != nil {
		endpoint.closeErr = errors.Join(endpoint.closeErr, endpoint.root.Close())
	}
	endpoint.closed = true
	return endpoint.closeErr
}
