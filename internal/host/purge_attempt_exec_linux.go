//go:build linux

package host

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"golang.org/x/sys/unix"
)

// This creator owns only the actual fixed child execution and code loans.
// Creation is not native hello acceptance, transaction or session authority.
// The real purpose receiver must select this object before observing a hello.
type purgeAttemptExecV1 struct {
	mu            sync.Mutex
	code          *purgeAttemptCodeV2
	start         *purgeAttemptStartV2
	creation      *os.File
	creationFD    int
	loan          *purgeAttemptCodeLoanV2
	command       *exec.Cmd
	process       *os.Process
	argv          []string
	peer          *nativeStartupPeerV1
	directory     *os.File
	line          *StartupLineV1
	args          api.PurgeArgs
	waitDone      chan struct{}
	waitErr       error
	stopCancel    func() bool
	cancelDone    chan struct{}
	cancelOnce    sync.Once
	cancelErr     error
	admissionSent bool // Set only at the actual private admission send, never by a setter.
	closed        bool
}

func startPurgeAttemptExecV1(ctx context.Context, code *purgeAttemptCodeV2, args api.PurgeArgs) (result *purgeAttemptExecV1, returned error) {
	if ctx == nil || code == nil || code.origin == nil || args.HostID != code.origin.hostID || !ValidHostID(args.HostID) || !ValidHostID(args.Nonce) {
		return nil, placementError("native attempt creation has no same actual code factory and authorised host")
	}
	owner := &purgeAttemptExecV1{code: code, args: args, creationFD: -1}
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
	if err := code.bindOriginalExecutionV2(ctx, owner); err != nil {
		return nil, err
	}
	owner.loan, err = code.borrowForOriginalExecV1(ctx)
	if err != nil {
		return nil, err
	}
	owner.directory, err = code.directory.Open(".")
	if err != nil {
		return nil, err
	}
	owner.start, err = newPurgeAttemptStartV2(owner)
	if err != nil {
		return nil, err
	}
	owner.command = owner.start.command
	owner.argv = slices.Clone(owner.start.argv)
	if err := owner.start.startActualV2(ctx); err != nil {
		return nil, err
	}
	if owner.creation == nil || owner.creationFD < 0 {
		return nil, placementError("the actual native attempt has no atomic original creation descriptor")
	}
	peerFD, err := unix.FcntlInt(owner.creation.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	process, err := os.OpenRoot("/proc/" + strconv.Itoa(owner.process.Pid))
	if err != nil {
		return nil, errors.Join(err, unix.Close(peerFD))
	}
	owner.peer = &nativeStartupPeerV1{pid: owner.process.Pid, uid: uint32(os.Getuid()), pidfd: peerFD, proc: process}
	owner.cancelDone = make(chan struct{})
	owner.stopCancel = context.AfterFunc(ctx, func() { defer close(owner.cancelDone); _ = owner.cancelRequestV2() })
	if err := owner.checkCreatorV1(ctx); err != nil {
		return nil, err
	}
	complete = true
	return owner, nil
}

// This is the mandatory public creation gate before the purpose receiver
// accepts the copy's native code, namespace and current sender observations.
func (owner *purgeAttemptExecV1) checkCreatorLockedV1(ctx context.Context) error {
	if owner == nil || ctx == nil || owner.closed || owner.code == nil || owner.loan == nil || owner.loan.creator != owner.code || owner.command == nil || owner.process == nil || owner.command.Process != owner.process || owner.peer == nil || owner.peer.pid != owner.process.Pid || owner.waitDone == nil || owner.directory == nil || owner.start == nil || owner.start.creator != owner || owner.creation == nil || owner.creationFD < 0 {
		return placementError("the pending purge hello has no original code factory and actual native creation")
	}
	if err := errors.Join(ctx.Err(), owner.start.checkFrozenV2(ctx), owner.peer.checkLive(), owner.code.directoryRef.matchesRoot(owner.code.directory), checkCreatedHelperArgumentsV1(owner.peer, owner.argv)); err != nil {
		return err
	}
	actual, err := owner.directory.Stat()
	if err != nil || !owner.code.directoryRef.matches(actual) {
		return errors.Join(placementError("the original attempt directory loan changed"), err)
	}
	return owner.peer.checkLive()
}

func (owner *purgeAttemptExecV1) checkCreatorV1(ctx context.Context) error {
	if owner == nil {
		return placementError("the original purge child creator is absent")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	return owner.checkCreatorLockedV1(ctx)
}

// Connect only through this factory's retained original directory. The child
// creates its own relative listener, so credentials name its actual creation.
// No hello is accepted here; H15's typed receiver must bind that observation.
func (owner *purgeAttemptExecV1) connectOriginalV1(ctx context.Context) error {
	if owner == nil {
		return placementError("the original purge child creator is absent")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if err := owner.checkCreatorLockedV1(ctx); err != nil {
		return err
	}
	if owner.line != nil {
		return placementError("the original purge attempt already has its sole bootstrap reader")
	}
	path := fmt.Sprintf("/proc/self/fd/%d/attempt.sock", owner.directory.Fd())
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return err
	}
	line, err := NewNativeStartupLineV1(connection)
	if err != nil {
		return errors.Join(err, connection.Close())
	}
	if err := owner.checkCreatorLockedV1(ctx); err != nil {
		return errors.Join(err, line.Close())
	}
	owner.line = line
	return nil
}

// Cancellation reaches an existing original request through its native
// completion object. It grants no stop, release or transaction authority.
func (owner *purgeAttemptExecV1) cancelRequestV2() error {
	if owner == nil {
		return nil
	}
	owner.cancelOnce.Do(func() {
		if owner.loan == nil || owner.loan.Cancel == nil {
			return
		}
		var value [8]byte
		binary.NativeEndian.PutUint64(value[:], 1)
		n, err := owner.loan.Cancel.Write(value[:])
		if err == nil && n != len(value) {
			err = placementError("the original attempt request received no complete native cancellation")
		}
		owner.cancelErr = err
	})
	return owner.cancelErr
}

// Close never kills a temporary owner after it could have captured native
// objects. Before admission, the original raw child joins its own bootstrap.
// After admission, only actual native exit permits these code loans to close.
func (owner *purgeAttemptExecV1) Close() error {
	if owner == nil {
		return nil
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed {
		return nil
	}
	if owner.stopCancel != nil && !owner.stopCancel() {
		<-owner.cancelDone
	}
	if err := owner.cancelRequestV2(); err != nil {
		return err
	}
	if owner.line != nil {
		_ = owner.line.conn.Close()
	}
	if owner.waitDone != nil {
		if owner.admissionSent {
			select {
			case <-owner.waitDone:
			default:
				return placementError("the actual temporary attempt still owns its original native subjects and cleanup")
			}
		} else {
			<-owner.waitDone
		}
	}
	if owner.process != nil {
		if err := owner.checkJoinedCreationV2(); err != nil {
			return err
		}
	}
	var failure error
	if owner.line != nil {
		failure = errors.Join(failure, owner.line.Close())
	}
	if owner.peer != nil {
		failure = errors.Join(failure, owner.peer.Close())
	}
	if owner.creation != nil {
		failure = errors.Join(failure, owner.creation.Close())
	}
	if owner.directory != nil {
		failure = errors.Join(failure, owner.directory.Close())
	}
	if owner.loan != nil {
		failure = errors.Join(failure, owner.loan.Close())
	}
	owner.closed = true
	return failure
}
