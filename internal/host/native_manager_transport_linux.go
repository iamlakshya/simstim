//go:build linux

package host

import (
	"errors"
	"net"
	"sync"
	"syscall"
	"time"
)

// This private byte-for-byte transport owns only actual I/O and the first
// close of one original Unix connection. It has no session or unit authority.
// Install it before NewConn so library cancellation/EOF uses the same owner.
type nativeManagerTransportV1 struct {
	connection *net.UnixConn
	retirement *nativeDescriptorRetirementsV1
	gate       sync.Mutex
	read       sync.Mutex
	write      sync.Mutex
	close      sync.Mutex
	users      sync.WaitGroup
	closing    bool
}

func newNativeManagerTransportV1(connection *net.UnixConn, retirement *nativeDescriptorRetirementsV1) *nativeManagerTransportV1 {
	return &nativeManagerTransportV1{connection: connection, retirement: retirement}
}

func (transport *nativeManagerTransportV1) beginV1() error {
	if transport == nil || transport.connection == nil || transport.retirement == nil {
		return net.ErrClosed
	}
	transport.gate.Lock()
	defer transport.gate.Unlock()
	if transport.closing {
		return net.ErrClosed
	}
	transport.users.Add(1)
	return nil
}

func (transport *nativeManagerTransportV1) Read(data []byte) (int, error) {
	if err := transport.beginV1(); err != nil {
		return 0, err
	}
	defer transport.users.Done()
	transport.read.Lock()
	defer transport.read.Unlock()
	return transport.connection.Read(data)
}

func (transport *nativeManagerTransportV1) Write(data []byte) (int, error) {
	if err := transport.beginV1(); err != nil {
		return 0, err
	}
	defer transport.users.Done()
	transport.write.Lock()
	defer transport.write.Unlock()
	return transport.connection.Write(data)
}

func (transport *nativeManagerTransportV1) SetDeadline(at time.Time) error {
	if err := transport.beginV1(); err != nil {
		return err
	}
	defer transport.users.Done()
	return transport.connection.SetDeadline(at)
}

func (transport *nativeManagerTransportV1) SetReadDeadline(at time.Time) error {
	if err := transport.beginV1(); err != nil {
		return err
	}
	defer transport.users.Done()
	return transport.connection.SetReadDeadline(at)
}

func (transport *nativeManagerTransportV1) SetWriteDeadline(at time.Time) error {
	if err := transport.beginV1(); err != nil {
		return err
	}
	defer transport.users.Done()
	return transport.connection.SetWriteDeadline(at)
}

func (transport *nativeManagerTransportV1) LocalAddr() net.Addr {
	return transport.connection.LocalAddr()
}
func (transport *nativeManagerTransportV1) RemoteAddr() net.Addr {
	return transport.connection.RemoteAddr()
}

func (transport *nativeManagerTransportV1) SyscallConn() (syscall.RawConn, error) {
	if err := transport.beginV1(); err != nil {
		return nil, err
	}
	defer transport.users.Done()
	raw, err := transport.connection.SyscallConn()
	if err != nil {
		return nil, err
	}
	return nativeManagerRawV1{transport: transport, raw: raw}, nil
}

type nativeManagerRawV1 struct {
	transport *nativeManagerTransportV1
	raw       syscall.RawConn
}

func (raw nativeManagerRawV1) Control(use func(uintptr)) error {
	if err := raw.transport.beginV1(); err != nil {
		return err
	}
	defer raw.transport.users.Done()
	return raw.raw.Control(use)
}

func (raw nativeManagerRawV1) Read(use func(uintptr) bool) error {
	if err := raw.transport.beginV1(); err != nil {
		return err
	}
	defer raw.transport.users.Done()
	return raw.raw.Read(use)
}

func (raw nativeManagerRawV1) Write(use func(uintptr) bool) error {
	if err := raw.transport.beginV1(); err != nil {
		return err
	}
	defer raw.transport.users.Done()
	return raw.raw.Write(use)
}

func (transport *nativeManagerTransportV1) isClosingV1() bool {
	if transport == nil {
		return true
	}
	transport.gate.Lock()
	defer transport.gate.Unlock()
	return transport.closing
}

func (transport *nativeManagerTransportV1) Close() error {
	if transport == nil || transport.connection == nil || transport.retirement == nil {
		return sessionLeaseError("the actual native manager transport has no original resource owner")
	}
	transport.close.Lock()
	defer transport.close.Unlock()
	transport.gate.Lock()
	transport.closing = true
	transport.gate.Unlock()
	// Deadlines interrupt original physical I/O without performing an
	// untracked close. Every admitted I/O/control ref returns before the
	// one profile-checked native close on this actual calling thread.
	_ = transport.connection.SetDeadline(time.Now())
	transport.users.Wait()
	return transport.retirement.closeConnectionV4(transport.connection)
}

func (transport *nativeManagerTransportV1) checkNativeRetiredV1() error {
	if transport == nil || transport.connection == nil || transport.retirement == nil {
		return sessionLeaseError("the manager transport has no original native retirement owner")
	}
	transport.close.Lock()
	defer transport.close.Unlock()
	return transport.retirement.checkConnectionV4(transport.connection)
}

func (transport *nativeManagerTransportV1) closeDiagnosticsV1() error {
	if transport == nil || transport.retirement == nil {
		return nil
	}
	return transport.retirement.diagnosticsV4()
}

var _ net.Conn = (*nativeManagerTransportV1)(nil)
var _ syscall.RawConn = nativeManagerRawV1{}

func nativeManagerDirectoryRetirementV1(manager *nativeUserManagerV1) error {
	if manager.directoryOriginal == nil {
		return nil
	}
	if manager.directoryClose == nil || !manager.directoryClose.Retired || manager.directoryClose.Unknown != nil {
		var unknown error
		if manager.directoryClose != nil {
			unknown = manager.directoryClose.Unknown
		}
		return errors.Join(sessionLeaseError("the original manager directory has no first native retirement proof"), unknown)
	}
	return nil
}
