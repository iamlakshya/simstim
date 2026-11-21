//go:build linux

package microvm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"

	"github.com/iamlakshya/simstim/internal/vmneed"
	"golang.org/x/sys/unix"
)

// Only the actual local sandbox native binding constructs this loan. Its
// retained handles originate in the original runner's creation/readiness
// transfer, never a PID lookup, pathname, Need or reopened metadata DTO.
type localCurrentVMMV1 struct {
	mu            sync.Mutex
	binding       *sandboxNativeBinding
	connection    net.Conn
	need          vmneed.NeedV1
	pidfd, socket *os.File
	sent, closed  bool
	closeErr      error
}

func (binding *sandboxNativeBinding) withLocalCurrentCopyVMMV1(ctx context.Context, connection net.Conn, expected vmneed.NeedV1, use func(LocalCurrentCopyVMMV1) error) (returned error) {
	if err := binding.checkNativeBinding(ctx, connection, expected); err != nil {
		return err
	}
	if err := binding.checkLocalLaunchOriginV1(ctx); err != nil {
		return err
	}
	loan := &localCurrentVMMV1{binding: binding, connection: connection, need: expected.Clone()}
	defer func() { returned = errors.Join(returned, binding.sandbox.closeOrKeepNativeOwnerV1(loan)) }()
	binding.proof.mu.Lock()
	if !binding.proof.ready || binding.proof.closed {
		binding.proof.mu.Unlock()
		return fmt.Errorf("the actual local VMM source ended before its original loan")
	}
	for index, file := range []*os.File{binding.proof.pidfd, binding.proof.socket} {
		if file == nil {
			binding.proof.mu.Unlock()
			return fmt.Errorf("the original local VMM has an absent native role")
		}
		fd, err := unix.FcntlInt(file.Fd(), unix.F_DUPFD_CLOEXEC, 3)
		if err != nil {
			binding.proof.mu.Unlock()
			return err
		}
		copy := os.NewFile(uintptr(fd), "actual-local-current-vmm")
		if index == 0 {
			loan.pidfd = copy
		} else {
			loan.socket = copy
		}
	}
	binding.proof.mu.Unlock()
	if err := errors.Join(checkNativeVMMPair(loan.pidfd, loan.socket), binding.checkNativeBinding(ctx, connection, expected)); err != nil {
		return err
	}
	result := use(loan)
	return errors.Join(result, binding.checkNativeBinding(ctx, connection, expected), binding.checkLocalLaunchOriginV1(ctx))
}

func (loan *localCurrentVMMV1) WithVMMFilesV1(ctx context.Context, send func(*os.File, *os.File) error) error {
	if loan == nil || ctx == nil || send == nil {
		return fmt.Errorf("the actual local VMM loan has no original sender")
	}
	loan.mu.Lock()
	defer loan.mu.Unlock()
	if loan.closed || loan.sent || loan.binding == nil || loan.pidfd == nil || loan.socket == nil {
		return fmt.Errorf("the original local VMM loan ended or was already sent")
	}
	if err := errors.Join(loan.binding.checkNativeBinding(ctx, loan.connection, loan.need), checkNativeVMMPair(loan.pidfd, loan.socket), loan.binding.checkLocalLaunchOriginV1(ctx)); err != nil {
		return err
	}
	loan.sent = true // A partial native send may already have loaned rights.
	result := send(loan.pidfd, loan.socket)
	return errors.Join(result, checkNativeVMMPair(loan.pidfd, loan.socket), loan.binding.checkNativeBinding(ctx, loan.connection, loan.need), loan.binding.checkLocalLaunchOriginV1(ctx))
}

func (loan *localCurrentVMMV1) Close() error {
	if loan == nil {
		return nil
	}
	loan.mu.Lock()
	defer loan.mu.Unlock()
	if loan.closed {
		return loan.closeErr
	}
	loan.closed = true
	for _, file := range []*os.File{loan.pidfd, loan.socket} {
		if file != nil {
			if loan.binding == nil || loan.binding.sandbox == nil {
				loan.closeErr = errors.Join(loan.closeErr, fmt.Errorf("the actual local VMM loan has no original descriptor owner"))
				continue
			}
			retired, err := loan.binding.sandbox.closeOwnedDescriptorV2(file)
			if !retired {
				loan.closeErr = errors.Join(loan.closeErr, err)
			}
		}
	}
	return loan.closeErr
}
