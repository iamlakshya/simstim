//go:build linux

package jail

import (
	"errors"
	"net"
	"os"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
	"golang.org/x/sys/unix"
)

// This closed original-source consumer preserves generic native peer checks.
// Its mapped UID/GID come from the SAME actually observed two-map C, never
// UIDMode, a Spec, a supplied PID or another namespace's numeric identity.
func (c *Cmd) CheckVMMReadonlyPeerV1(source *supply.ExecModeLeaseV1, connection *net.UnixConn, received *proto.UnixControlV1) error {
	if received == nil || len(received.Files) != 2 {
		return vmmModeFailureV1("the original read-only readiness lacks its two actual roles")
	}
	if err := received.CheckReceivedV1(connection); err != nil {
		return err
	}
	if err := c.CheckVMMNativeSenderV1(source, connection, received.Credentials); err != nil {
		return err
	}
	return c.CheckVMMReadonlyPairV1(source, received.Files[0], received.Files[1])
}

func (c *Cmd) CheckVMMReadonlyPairV1(source *supply.ExecModeLeaseV1, process, socket *os.File) error {
	owner, err := c.vmmOwnerV1(source)
	if err != nil {
		return err
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if !owner.readonly || !owner.finalProved || owner.readonlyPhysicalClosed || owner.init == nil || owner.runner == nil || process == nil || socket == nil {
		return vmmModeFailureV1("the original read-only VM has no live final-code/creation scope")
	}
	if err := owner.root.live(); err != nil {
		return err
	}
	if err := owner.init.checkExecutableV1(owner.program); err != nil {
		return err
	}
	if err := owner.init.checkNamespacesV1(owner.init.namespaces); err != nil {
		return err
	}
	if err := owner.checkReadonlyIDsV1(owner.init, false); err != nil {
		return err
	}
	pid, _, err := vmmPIDFDIdentityV1(process)
	if err != nil || pid != owner.init.pid {
		return errors.Join(err, vmmModeFailureV1("the readiness handle is not this original final init"))
	}
	raw, err := socket.SyscallConn()
	if err != nil {
		return err
	}
	var native error
	err = raw.Control(func(fd uintptr) {
		kind, err := unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_TYPE)
		if err != nil || kind != unix.SOCK_STREAM {
			native = errors.Join(err, vmmModeFailureV1("the original guest peer is not a native stream"))
			return
		}
		peer, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if err != nil || peer == nil || int(peer.Pid) != owner.init.pid || peer.Uid != owner.subUID || peer.Gid != owner.subGID {
			native = errors.Join(err, vmmModeFailureV1("the guest peer differs from the actual original mapped init"))
			return
		}
		poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN | unix.POLLRDHUP}}
		_, err = unix.Poll(poll, 0)
		if err != nil || poll[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLRDHUP|unix.POLLNVAL) != 0 {
			native = errors.Join(err, vmmModeFailureV1("the original guest stream ended"))
			return
		}
		var peek [1]byte
		n, _, err := unix.Recvfrom(int(fd), peek[:], unix.MSG_PEEK|unix.MSG_DONTWAIT)
		if err != unix.EAGAIN && err != unix.EWOULDBLOCK && (err != nil || n == 0) {
			native = errors.Join(err, vmmModeFailureV1("the original guest stream has no live native peer"))
		}
	})
	if err != nil || native != nil {
		return errors.Join(err, native)
	}
	if err := owner.init.live(); err != nil {
		return err
	}
	_, _, err = vmmPIDFDIdentityV1(process)
	return err
}
