//go:build linux

package host

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"golang.org/x/sys/unix"
)

// The raw child creates this own endpoint. It does not adopt an endpoint or
// directory from a caller's pathname. A relative bind avoids HOME path limits.
type purgeOwnedListenerV3 struct {
	mu         sync.Mutex
	acceptMu   sync.Mutex
	retirement purgeNativeRetirementsV4
	socketGone bool
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

// Accept belongs to this original listener. Close interrupts its deadline and
// joins this actual gate before recording the first native listener close.
func (owner *purgeOwnedListenerV3) acceptV4() (*net.UnixConn, error) {
	owner.acceptMu.Lock()
	defer owner.acceptMu.Unlock()
	owner.mu.Lock()
	closing := owner.closing || owner.closed
	owner.mu.Unlock()
	if closing {
		return nil, placementError("the original owned listener is closing")
	}
	return owner.listener.AcceptUnix()
}

func (owner *purgeOwnedListenerV3) Close() (returned error) {
	if owner == nil {
		return nil
	}
	defer func() { returned = errors.Join(returned, owner.retirement.interrupts.diagnosticsV4()) }()
	owner.mu.Lock()
	owner.closing = true
	listener := owner.listener
	owner.mu.Unlock()
	if listener != nil {
		owner.retirement.interruptListenerV4(listener)
	}
	owner.acceptMu.Lock()
	defer owner.acceptMu.Unlock()
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed {
		return errors.Join(owner.closeErr, owner.retirement.diagnosticsV4(), owner.retirement.checkAllRetiredV4())
	}
	owner.closeErr = errors.Join(owner.closeErr, owner.retirement.closeListenerV4(listener))
	if err := owner.retirement.checkListenerV4(listener); err != nil {
		return errors.Join(owner.closeErr, err)
	}
	if listener != nil && owner.socket == nil {
		return errors.Join(owner.closeErr, placementError("the actual socket creation has no retained native identity; preserve its owner"))
	}
	if !owner.socketGone && owner.root != nil && owner.socket != nil {
		info, err := owner.root.Lstat("attempt.sock")
		if err == nil && os.SameFile(info, owner.socket) {
			if err := owner.root.Remove("attempt.sock"); err != nil {
				return errors.Join(owner.closeErr, err)
			}
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
		} else {
			return errors.Join(owner.closeErr, placementError("preserved an unrelated replacement purge socket"))
		}
		if err := syncCapturedDirectory(owner.root); err != nil {
			return errors.Join(owner.closeErr, err)
		}
		owner.socketGone = true
	}
	owner.closeErr = errors.Join(owner.closeErr, owner.retirement.closeFilesV4([]*os.File{owner.socketFile}), owner.retirement.closeRootsV4([]*os.Root{owner.root}))
	native := owner.retirement.checkAllRetiredV4()
	if native == nil {
		owner.closed = true
	}
	return errors.Join(owner.closeErr, owner.retirement.diagnosticsV4(), native)
}

func (owner *purgeOwnedListenerV3) checkNativeRetiredV3(ctx context.Context) error {
	if owner == nil || ctx == nil {
		return placementError("the original owned listener retirement is absent")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if !owner.closed || owner.listener != nil && !owner.socketGone {
		return placementError("the actual original listener retains unknown native resources")
	}
	return owner.retirement.checkAllRetiredV4()
}

func (owner *purgeOwnedListenerV3) CloseDiagnosticsV3() error {
	if owner == nil {
		return nil
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	return errors.Join(owner.closeErr, owner.retirement.diagnosticsV4())
}
