package guest

import (
	"net"
	"os"
	"syscall"

	"github.com/iamlakshya/simstim/internal/proto"
)

// SocketPair makes a pair of connected Unix sockets: ours as a connection,
// and theirs as a file for a child's ExtraFiles. Neither end leaks into
// another child: the child gets theirs as a copy, on the fd that
// ExtraFiles gives it. The caller closes theirs once the child has started.
func SocketPair() (ours net.Conn, theirs *os.File, err error) {
	syscall.ForkLock.RLock()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err == nil {
		syscall.CloseOnExec(fds[0])
		syscall.CloseOnExec(fds[1])
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, nil, &proto.Error{Code: "wall_setup", Message: "cannot make a socket pair: " + err.Error(),
			Hint: "Check the host's limit on open files, then resume the session."}
	}
	f := os.NewFile(uintptr(fds[0]), "link")
	ours, err = net.FileConn(f)
	f.Close()
	if err != nil {
		syscall.Close(fds[1])
		return nil, nil, &proto.Error{Code: "wall_setup", Message: "cannot use a socket pair: " + err.Error(),
			Hint: "Check the host's limit on open files, then resume the session."}
	}
	return ours, os.NewFile(uintptr(fds[1]), "link-peer"), nil
}
