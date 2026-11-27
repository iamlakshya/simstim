//go:build linux

package host

import (
	"errors"
	"net"
	"os"
	"strconv"
	"sync"

	"github.com/iamlakshya/simstim/internal/secretmem"
	"golang.org/x/sys/unix"
)

// nativeHelperIdentityV1 is an owned SAME-line native handoff from the actual
// code producer. Public code/namespace handles are not a private OP loan.
// The original creation pidfd/first native exec and current sender still have
// to bind this identity before the outside owner releases any private rights.
type nativeHelperIdentityV1 struct {
	expected                 os.FileInfo
	mu                       sync.RWMutex
	executable, user, cgroup *os.File
	closed                   bool
}

func (identity *nativeHelperIdentityV1) Check(expected os.FileInfo) error {
	if identity == nil {
		return sessionLeaseError("the original native helper identity is absent")
	}
	identity.mu.RLock()
	defer identity.mu.RUnlock()
	if identity.closed || identity.executable == nil || identity.user == nil || identity.cgroup == nil {
		return sessionLeaseError("the original native helper identity ended")
	}
	actual, err := identity.executable.Stat()
	if err != nil || expected == nil || !os.SameFile(expected, actual) {
		return errors.Join(sessionLeaseError("the actual native helper code differs from its original mapped object"), err)
	}
	for _, role := range []struct {
		file *os.File
		kind int
		self string
	}{{identity.user, unix.CLONE_NEWUSER, "/proc/self/ns/user"}, {identity.cgroup, unix.CLONE_NEWCGROUP, "/proc/self/ns/cgroup"}} {
		var filesystem unix.Statfs_t
		if err := unix.Fstatfs(int(role.file.Fd()), &filesystem); err != nil || filesystem.Type != unix.NSFS_MAGIC {
			return errors.Join(sessionLeaseError("the original helper namespace role is not a native namespace"), err)
		}
		kind, err := unix.IoctlRetInt(int(role.file.Fd()), unix.NS_GET_NSTYPE)
		if err != nil || kind != role.kind {
			return errors.Join(sessionLeaseError("the original helper namespace role has another native type"), err)
		}
		self, err := os.Open(role.self)
		if err != nil {
			return err
		}
		ours, statErr := self.Stat()
		theirs, peerErr := role.file.Stat()
		closeErr := self.Close()
		if statErr != nil || peerErr != nil || !os.SameFile(ours, theirs) {
			return errors.Join(sessionLeaseError("the original helper is in another native host namespace"), statErr, peerErr, closeErr)
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
func (identity *nativeHelperIdentityV1) Close() error {
	if identity == nil {
		return nil
	}
	identity.mu.Lock()
	defer identity.mu.Unlock()
	if identity.closed {
		return nil
	}
	identity.closed = true
	return errors.Join(identity.executable.Close(), identity.user.Close(), identity.cgroup.Close())
}
func currentNativeHelperIdentityV1() (_ *nativeHelperIdentityV1, returned error) {
	if err := secretmem.Protect(); err != nil {
		return nil, err
	}
	result := &nativeHelperIdentityV1{}
	complete := false
	defer func() {
		if !complete {
			for _, file := range []*os.File{result.executable, result.user, result.cgroup} {
				if file != nil {
					returned = errors.Join(returned, file.Close())
				}
			}
		}
	}()
	var err error
	result.executable, err = os.Open("/proc/self/exe")
	if err != nil {
		return nil, err
	}
	result.user, err = os.Open("/proc/self/ns/user")
	if err != nil {
		return nil, err
	}
	result.cgroup, err = os.Open("/proc/self/ns/cgroup")
	if err != nil {
		return nil, err
	}
	expected, err := result.executable.Stat()
	if err != nil {
		return nil, err
	}
	if err := result.Check(expected); err != nil {
		return nil, err
	}
	complete = true
	return result, nil
}
func pinUnverifiedNativeHelperPeerV1(connection net.Conn) (*nativeStartupPeerV1, error) {
	actual, err := peerOf(connection)
	if err != nil {
		return nil, err
	}
	if actual.done != nil {
		defer actual.done()
	}
	if actual.pid <= 0 || actual.uid != os.Getuid() {
		return nil, sessionLeaseError("the original native helper peer has another account")
	}
	descriptor, err := unix.PidfdOpen(actual.pid, 0)
	if err != nil {
		return nil, err
	}
	process, err := os.OpenRoot("/proc/" + strconv.Itoa(actual.pid))
	if err != nil {
		return nil, errors.Join(err, unix.Close(descriptor))
	}
	result := &nativeStartupPeerV1{pid: actual.pid, uid: uint32(actual.uid), pidfd: descriptor, proc: process}
	if actual.alive != nil && !actual.alive() {
		return nil, errors.Join(sessionLeaseError("the original native helper peer already ended"), result.Close())
	}
	if err := result.checkLive(); err != nil {
		return nil, errors.Join(err, result.Close())
	}
	return result, nil
}
