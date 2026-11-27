//go:build linux

package host

import (
	"errors"
	"net"
	"os"
	"runtime"
	"syscall"

	"github.com/iamlakshya/simstim/internal/supply"
	"golang.org/x/sys/unix"
)

func checkOriginalNativeUnixHandleV1(raw syscall.RawConn) error {
	var native error
	err := raw.Control(func(fd uintptr) {
		var stat unix.Stat_t
		if err := unix.Fstat(int(fd), &stat); err != nil {
			native = err
			return
		}
		domain, err := unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_DOMAIN)
		if err != nil || stat.Mode&unix.S_IFMT != unix.S_IFSOCK || domain != unix.AF_UNIX {
			native = errors.Join(placementError("the original Unix close has no live native socket"), err)
		}
	})
	return errors.Join(err, native)
}

func originalNativeUnixCloseFactsV1(err error) supply.FileCloseFactsV1 {
	facts := supply.FileCloseFactsV1{Diagnostic: err}
	if err == nil {
		facts.Retired = true
		return facts
	}
	var operation *net.OpError
	var errno syscall.Errno
	if !errors.Is(err, net.ErrClosed) && !errors.Is(err, os.ErrClosed) && errors.As(err, &operation) && operation.Op == "close" && errors.As(operation.Err, &errno) && errno != 0 && errno != syscall.EBADF {
		facts.Retired = true
		return facts
	}
	facts.Unknown = errors.Join(err, placementError("the first original Unix close has unknown native retirement"))
	return facts
}

// The concrete owner must first join all actual I/O users. The StartupLine
// method below enforces its real read/write gates before entering this call.
func (owner *nativeDescriptorRetirementsV1) closeConnectionV4(connection *net.UnixConn) error {
	if connection == nil {
		return nil
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.connections == nil {
		owner.connections = make(map[*net.UnixConn]supply.FileCloseFactsV1)
	}
	facts, attempted := owner.connections[connection]
	if !attempted {
		owner.connections[connection] = nativeUnattemptedRetirementV1()
		runtime.LockOSThread()
		profile := supply.CheckFileCloseThreadV1()
		raw, err := connection.SyscallConn()
		if profile == nil && err == nil {
			err = checkOriginalNativeUnixHandleV1(raw)
		}
		if profile != nil || err != nil {
			facts = supply.FileCloseFactsV1{Unknown: errors.Join(profile, err)}
		} else {
			facts = originalNativeUnixCloseFactsV1(connection.Close())
		}
		runtime.UnlockOSThread()
		owner.connections[connection] = facts
	}
	return errors.Join(facts.Diagnostic, nativeCheckRetirementFactsV1(facts))
}

func (owner *nativeDescriptorRetirementsV1) checkConnectionV4(connection *net.UnixConn) error {
	if connection == nil {
		return nil
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	facts, found := owner.connections[connection]
	if !found {
		return placementError("the original Unix connection has no first native close record")
	}
	return nativeCheckRetirementFactsV1(facts)
}

// Accept must already have physically returned to this actual listener owner.
func (owner *nativeDescriptorRetirementsV1) closeListenerV4(listener *net.UnixListener) error {
	if listener == nil {
		return nil
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.listeners == nil {
		owner.listeners = make(map[*net.UnixListener]supply.FileCloseFactsV1)
	}
	facts, attempted := owner.listeners[listener]
	if !attempted {
		owner.listeners[listener] = nativeUnattemptedRetirementV1()
		runtime.LockOSThread()
		profile := supply.CheckFileCloseThreadV1()
		raw, err := listener.SyscallConn()
		if profile == nil && err == nil {
			err = checkOriginalNativeUnixHandleV1(raw)
		}
		if profile != nil || err != nil {
			facts = supply.FileCloseFactsV1{Unknown: errors.Join(profile, err)}
		} else {
			facts = originalNativeUnixCloseFactsV1(listener.Close())
		}
		runtime.UnlockOSThread()
		owner.listeners[listener] = facts
	}
	return errors.Join(facts.Diagnostic, nativeCheckRetirementFactsV1(facts))
}

func (owner *nativeDescriptorRetirementsV1) checkListenerV4(listener *net.UnixListener) error {
	if listener == nil {
		return nil
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	facts, found := owner.listeners[listener]
	if !found {
		return placementError("the actual listener has no first native close record")
	}
	return nativeCheckRetirementFactsV1(facts)
}
