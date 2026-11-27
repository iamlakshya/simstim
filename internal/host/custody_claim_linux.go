//go:build linux

package host

import (
	"errors"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

// custodyClaimV1 owns a peer-closed kernel receive queue. Dup aliases share
// its one queued packet; neither an alias nor a reopened proc path can refill
// it. This primitive alone grants no session, source or execution authority.
type custodyClaimV1 struct {
	mu         sync.Mutex
	file       *os.File
	closed     bool
	seed       *os.File
	retirement nativeStartupRetirementsV1
}

func newCustodyClaimV1() (partial *custodyClaimV1, returned error) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	read := os.NewFile(uintptr(pair[0]), "native-custody-claim")
	write := os.NewFile(uintptr(pair[1]), "native-custody-claim-seed")
	if read == nil || write == nil {
		return nil, errors.Join(sessionLeaseError("the native custody claim has no kernel socket pair"), unix.Close(pair[0]), unix.Close(pair[1]))
	}
	claim := &custodyClaimV1{file: read, seed: write}
	complete := false
	defer func() {
		if !complete {
			cleanup := claim.Close()
			returned = errors.Join(returned, cleanup, claim.closeDiagnosticsV1())
			if cleanup != nil {
				partial = claim
			}
		}
	}()
	count, err := unix.SendmsgN(int(write.Fd()), []byte{1}, nil, nil, unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL)
	_ = claim.retirement.closeFilesV4([]*os.File{write})
	closed := claim.retirement.checkFilesV4([]*os.File{write})
	if err != nil || closed != nil || count != 1 {
		return nil, errors.Join(sessionLeaseError("the native custody claim could not seed its one kernel packet"), err, closed)
	}
	if err := claim.peek(); err != nil {
		return nil, err
	}
	complete = true
	return claim, nil
}

func (claim *custodyClaimV1) receive(peek bool) error {
	if claim == nil {
		return sessionLeaseError("the original native custody claim is absent")
	}
	claim.mu.Lock()
	defer claim.mu.Unlock()
	if claim.closed || claim.file == nil {
		return sessionLeaseError("the original native custody claim ended")
	}
	kind, err := unix.GetsockoptInt(int(claim.file.Fd()), unix.SOL_SOCKET, unix.SO_TYPE)
	if err != nil || kind != unix.SOCK_SEQPACKET {
		return errors.Join(sessionLeaseError("the native custody claim has another kernel socket type"), err)
	}
	domain, err := unix.GetsockoptInt(int(claim.file.Fd()), unix.SOL_SOCKET, unix.SO_DOMAIN)
	if err != nil || domain != unix.AF_UNIX {
		return errors.Join(sessionLeaseError("the native custody claim has another kernel socket domain"), err)
	}
	var stat unix.Stat_t
	var fs unix.Statfs_t
	if err := unix.Fstat(int(claim.file.Fd()), &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFSOCK {
		return errors.Join(sessionLeaseError("the native custody claim is not a socket object"), err)
	}
	if err := unix.Fstatfs(int(claim.file.Fd()), &fs); err != nil || fs.Type != unix.SOCKFS_MAGIC {
		return errors.Join(sessionLeaseError("the native custody claim is not the original sockfs object"), err)
	}
	// Do not call recv before verifying peer closure. A live peer could refill
	// the queue after a successful claim and issue a second alias owner.
	polls := []unix.PollFd{{Fd: int32(claim.file.Fd()), Events: unix.POLLIN | unix.POLLHUP}}
	ready, err := unix.Poll(polls, 0)
	if err != nil || ready != 1 || polls[0].Revents&unix.POLLHUP == 0 || polls[0].Revents&unix.POLLIN == 0 {
		return errors.Join(sessionLeaseError("the native custody claim is not a closed-peer queued packet"), err)
	}
	flags := unix.MSG_DONTWAIT | unix.MSG_CMSG_CLOEXEC
	if peek {
		flags |= unix.MSG_PEEK
	}
	var payload [2]byte
	var control [128]byte
	n, oob, received, _, err := unix.Recvmsg(int(claim.file.Fd()), payload[:], control[:], flags)
	if oob > 0 {
		messages, parseErr := unix.ParseSocketControlMessage(control[:oob])
		for _, message := range messages {
			if message.Header.Level == unix.SOL_SOCKET && message.Header.Type == unix.SCM_RIGHTS {
				rights, rightsErr := unix.ParseUnixRights(&message)
				parseErr = errors.Join(parseErr, rightsErr)
				for _, descriptor := range rights {
					parseErr = errors.Join(parseErr, unix.Close(descriptor))
				}
			}
		}
		err = errors.Join(err, parseErr)
	}
	if err != nil || n != 1 || payload[0] != 1 || oob != 0 || received&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 {
		return errors.Join(sessionLeaseError("the original native custody claim is missing, consumed or malformed"), err)
	}
	return nil
}

func (claim *custodyClaimV1) peek() error    { return claim.receive(true) }
func (claim *custodyClaimV1) consume() error { return claim.receive(false) }

// Close releases this alias only; other actual kernel aliases remain fenced.
func (claim *custodyClaimV1) Close() error {
	if claim == nil {
		return nil
	}
	claim.mu.Lock()
	defer claim.mu.Unlock()
	claim.closed = true
	files := []*os.File{claim.file, claim.seed}
	_ = claim.retirement.closeFilesV4(files)
	return claim.retirement.checkFilesV4(files)
}

func (claim *custodyClaimV1) closeDiagnosticsV1() error {
	if claim == nil {
		return nil
	}
	return claim.retirement.diagnosticsV4()
}
