//go:build linux

package host

import (
	"context"
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
	issuer          *purgePrivateEndpointV2
	retirement      purgeNativeRetirementsV4
	cleanupComplete bool
	root            *os.Root
	directory       *os.File
	reference       cachefs.Reference
	listener        *net.UnixListener
	socket          os.FileInfo
	socketFile      *os.File
	closed          bool
	closeErr        error
	leaf            string
	path            string
}

func newPurgePrivateEndpointV2() (result *purgePrivateEndpointV2, returned error) {
	nonce, err := footprintNonce()
	if err != nil {
		return nil, err
	}
	endpoint := &purgePrivateEndpointV2{leaf: ".pg-" + nonce[:16]}
	endpoint.issuer = endpoint
	complete := false
	defer func() {
		if !complete {
			closeErr := endpoint.Close()
			returned = errors.Join(returned, closeErr)
			if closeErr != nil {
				result = endpoint
			}
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
	if endpoint.issuer != endpoint {
		return sessionLeaseError("the private endpoint has no original native creator")
	}
	if !endpoint.cleanupComplete {
		listenerErr := endpoint.retirement.closeListenerV4(endpoint.listener)
		if native := endpoint.retirement.checkListenerV4(endpoint.listener); native != nil {
			return errors.Join(listenerErr, native)
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
				// The SAME still-open original socket proves the prior unlink.
			} else {
				return errors.Join(sessionLeaseError("the actual private endpoint unlink is unknown or replaced"), err)
			}
			if err := syncCapturedDirectory(endpoint.root); err != nil {
				return err
			}
		}
		endpoint.cleanupComplete = true
	}
	closedFiles := endpoint.retirement.closeFilesV4([]*os.File{endpoint.socketFile, endpoint.directory})
	closedRoots := endpoint.retirement.closeRootsV4([]*os.Root{endpoint.root})
	native := endpoint.checkNativeRetiredV3(context.Background())
	if native == nil {
		endpoint.closed = true
	}
	endpoint.closeErr = endpoint.retirement.diagnosticsV4()
	return errors.Join(closedFiles, closedRoots, native, endpoint.closeErr)
}

func (endpoint *purgePrivateEndpointV2) checkNativeRetiredV3(ctx context.Context) error {
	if endpoint == nil || ctx == nil || endpoint.issuer != endpoint || !endpoint.cleanupComplete {
		return sessionLeaseError("the private endpoint has no original physically completed cleanup")
	}
	return errors.Join(ctx.Err(), endpoint.retirement.checkListenerV4(endpoint.listener), endpoint.retirement.checkFilesV4([]*os.File{endpoint.socketFile, endpoint.directory}), endpoint.retirement.checkRootsV4([]*os.Root{endpoint.root}))
}

func (endpoint *purgePrivateEndpointV2) CloseDiagnosticsV3() error {
	if endpoint == nil {
		return nil
	}
	return endpoint.retirement.diagnosticsV4()
}
