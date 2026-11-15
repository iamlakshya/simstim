//go:build linux

package guest

import (
	"context"
	"net"
	"os"

	"github.com/iamlakshya/simstim/internal/link"
	"golang.org/x/sys/unix"
)

// vmInitConnection retains only the actual verified init's original native
// connection. These necessary facts do not issue original VM Need authority.
// That separate operation-bound receipt must join this same boot and Init.
type vmInitConnection struct {
	owner   context.Context
	init    *Init
	session *link.Session
	native  *vmSocketConn
}

// newVMInitConnection is called only by runMicroVM after its entry, tools
// and disks passed verification and acceptVMGuest accepted one CID_HOST peer.
// It cannot be constructed by a control DTO, session ID, ABI or memory fact.
func newVMInitConnection(owner context.Context, init *Init, connection net.Conn) (*vmInitConnection, error) {
	native, ok := connection.(*vmSocketConn)
	if !ok || init == nil {
		return nil, vmError("VM preparation has no original native init connection")
	}
	result := &vmInitConnection{owner: owner, init: init, session: init.sess, native: native}
	if err := result.check(init); err != nil {
		return nil, err
	}
	return result, nil
}

func (o *vmInitConnection) check(init *Init) error {
	if o == nil || init == nil || o.init != init || o.session == nil || init.sess != o.session || o.native == nil || o.owner == nil || init.lifetime == nil {
		return vmError("VM preparation has no same-init connection owner")
	}
	if os.Getpid() != 1 || os.Geteuid() != 0 {
		return vmError("the VM connection owner is not the actual fixed PID1 init")
	}
	if err := o.owner.Err(); err != nil {
		return err
	}
	if err := init.lifetime.Err(); err != nil {
		return err
	}
	select {
	case <-o.session.Done():
		return vmError("the original VM init connection has ended")
	default:
	}
	// SyscallConn retains the same open file description during the check;
	// a closed/reused numeric descriptor cannot replace this native peer.
	raw, err := o.native.SyscallConn()
	if err != nil {
		return vmError("the original VM init socket is unavailable")
	}
	var peerErr error
	peerIsHost := false
	if err := raw.Control(func(fd uintptr) {
		var peer unix.Sockaddr
		peer, peerErr = unix.Getpeername(int(fd))
		if address, ok := peer.(*unix.SockaddrVM); ok && address.CID == unix.VMADDR_CID_HOST {
			peerIsHost = true
		}
	}); err != nil || peerErr != nil || !peerIsHost {
		return vmError("the retained VM connection is not the original native host peer")
	}
	if err := o.owner.Err(); err != nil {
		return err
	}
	return init.lifetime.Err()
}
