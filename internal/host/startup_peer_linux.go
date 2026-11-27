//go:build linux

package host

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"golang.org/x/sys/unix"
)

type nativeStartupPeerV1 struct {
	identity *nativeHelperIdentityV1
	pid      int
	uid      uint32
	pidfd    int
	proc     *os.Root
	once     sync.Once
	closed   bool
	closeErr error
	mu       sync.Mutex
}

type pendingStartupV1 struct {
	owner *OwnedSupervisorLease
	line  *StartupLineV1
	peer  *nativeStartupPeerV1
	nonce string
}

// PrepareStartupHello pins the actual outside connector before replying with
// a fresh challenge. On Linux 6.1 the still-live pidfd, kernel SCM credentials
// and response to this fresh challenge bind the sender without SO_PEERPIDFD.
func (owner *OwnedSupervisorLease) PrepareStartupHello(ctx context.Context, line *StartupLineV1) (string, error) {
	if owner == nil || owner.native == nil || ctx == nil || line == nil {
		return "", sessionLeaseError("the pending supervisor has no original startup owner")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if !owner.Valid(owner.native.id) || owner.startup != nil {
		return "", sessionLeaseError("the supervisor already has an original startup connector")
	}
	if err := errors.Join(ctx.Err(), line.RejectAncillary()); err != nil {
		return "", err
	}
	line.mu.Lock()
	peer := line.nativePeer
	line.mu.Unlock()
	if peer == nil {
		return "", sessionLeaseError("the original outside startup has no accepted native code/namespace hello")
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", errors.Join(err, peer.Close())
	}
	if err := peer.Check(); err != nil {
		return "", errors.Join(err, peer.Close())
	}
	line.mu.Lock()
	defer line.mu.Unlock()
	if line.closed || line.candidate != nil {
		return "", errors.Join(sessionLeaseError("the candidate startup line ended or already has a challenge"), peer.Close())
	}
	line.candidate = &pendingStartupV1{owner: owner, line: line, peer: peer, nonce: hex.EncodeToString(nonce[:])}
	return line.candidate.nonce, nil
}

func pinOutsideStartupPeer(conn net.Conn) (*nativeStartupPeerV1, error) {
	peer, err := peerOf(conn)
	if err != nil {
		return nil, err
	}
	if peer.done != nil {
		defer peer.done()
	}
	if peer.pid <= 0 || peer.uid != os.Getuid() {
		return nil, sessionLeaseError("startup connector has another native account")
	}
	pidfd, err := unix.PidfdOpen(peer.pid, 0)
	if err != nil {
		return nil, err
	}
	proc, err := os.OpenRoot(filepath.Join(procRoot, strconv.Itoa(peer.pid)))
	if err != nil {
		return nil, errors.Join(err, unix.Close(pidfd))
	}
	owned := &nativeStartupPeerV1{pid: peer.pid, uid: uint32(peer.uid), pidfd: pidfd, proc: proc}
	if peer.alive != nil && !peer.alive() {
		return nil, errors.Join(sessionLeaseError("the original startup connector ended"), owned.Close())
	}
	if err := owned.Check(); err != nil {
		return nil, errors.Join(err, owned.Close())
	}
	return owned, nil
}

func pollNonBlocking(polls []unix.PollFd) (int, error) {
	for {
		n, err := unix.Poll(polls, 0)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return n, fmt.Errorf("poll pidfd: %w", err)
		}
		return n, nil
	}
}

func (peer *nativeStartupPeerV1) checkLocked() error {
	if peer == nil || peer.closed || peer.pidfd < 0 || peer.proc == nil {
		return sessionLeaseError("the original startup connector ended")
	}
	polls := []unix.PollFd{{Fd: int32(peer.pidfd), Events: unix.POLLIN}}
	n, err := pollNonBlocking(polls)
	if err != nil || n != 0 {
		return errors.Join(sessionLeaseError("the original startup connector is no longer live"), err)
	}
	cgroup, err := peer.proc.ReadFile("cgroup")
	paths, _ := cgroupPaths(cgroup)
	if err != nil || len(paths) == 0 || placeAmong(paths, "") != placeOutside {
		return errors.Join(sessionLeaseError("the startup connector is inside a session or has unknown placement"), err)
	}
	if peer.identity != nil {
		if err := peer.identity.Check(peer.identity.expected); err != nil {
			return err
		}
	} else {
		selfNS, err := os.Readlink(filepath.Join(procRoot, "self", "ns", "cgroup"))
		if err != nil {
			return err
		}
		peerNS, err := peer.proc.Readlink("ns/cgroup")
		if err != nil || selfNS != peerNS {
			return errors.Join(sessionLeaseError("the original startup connector changed cgroup namespace"), err)
		}
	}
	n, err = pollNonBlocking(polls)
	if err != nil || n != 0 {
		return errors.Join(sessionLeaseError("the startup connector ended during its native placement check"), err)
	}
	return nil
}

func (peer *nativeStartupPeerV1) Check() error {
	peer.mu.Lock()
	defer peer.mu.Unlock()
	return peer.checkLocked()
}

func (peer *nativeStartupPeerV1) CheckSender(sender startupCredentials) error {
	peer.mu.Lock()
	defer peer.mu.Unlock()
	if sender.pid != peer.pid || sender.uid != peer.uid {
		return sessionLeaseError("the current native loan sender differs from the original outside connector")
	}
	return peer.checkLocked()
}

func (peer *nativeStartupPeerV1) Close() error {
	if peer == nil {
		return nil
	}
	peer.once.Do(func() {
		peer.mu.Lock()
		defer peer.mu.Unlock()
		peer.closed = true
		peer.closeErr = unix.Close(peer.pidfd)
		if peer.identity != nil {
			peer.closeErr = errors.Join(peer.closeErr, peer.identity.Close())
		}
		if peer.proc != nil {
			peer.closeErr = errors.Join(peer.closeErr, peer.proc.Close())
		}
	})
	return peer.closeErr
}

func (peer *nativeStartupPeerV1) checkLive() error {
	if peer == nil {
		return sessionLeaseError("the original native startup process ended")
	}
	peer.mu.Lock()
	defer peer.mu.Unlock()
	if peer.closed || peer.pidfd < 0 || peer.proc == nil {
		return sessionLeaseError("the original native startup handle ended")
	}
	polls := []unix.PollFd{{Fd: int32(peer.pidfd), Events: unix.POLLIN}}
	n, err := pollNonBlocking(polls)
	if err != nil || n != 0 {
		return errors.Join(sessionLeaseError("the original native startup process ended"), err)
	}
	return nil
}

func (peer *nativeStartupPeerV1) checkNativeSender(sender startupCredentials) error {
	if peer == nil || sender.pid != peer.pid || sender.uid != peer.uid {
		return sessionLeaseError("the current native startup sender differs from its original live owner")
	}
	return peer.checkLive()
}
