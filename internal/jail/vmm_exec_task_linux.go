//go:build linux

package jail

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// These checks resolve only below an already-held native task object while
// its SAME creation pidfd is live. Received code/namespace bags cannot replace
// the executing actor's own current executable and native namespaces.
func (actor *vmmExecActorV1) checkExecutableV1(expected *os.File) error {
	if err := actor.live(); err != nil {
		return err
	}
	fd, err := unix.Openat(int(actor.task.Fd()), "exe", unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var actual, want unix.Stat_t
	if err := errors.Join(unix.Fstat(fd, &actual), unix.Fstat(int(expected.Fd()), &want)); err != nil {
		return err
	}
	if actual.Mode&unix.S_IFMT != unix.S_IFREG || actual.Dev != want.Dev || actual.Ino != want.Ino || actual.Size != want.Size {
		return vmmModeFailureV1("the original live task runs another actual executable")
	}
	return actor.live()
}
func (actor *vmmExecActorV1) checkNamespacesV1(expected [3]os.FileInfo) error {
	if err := actor.live(); err != nil {
		return err
	}
	for index, name := range []string{"user", "pid", "mnt"} {
		fd, err := unix.Openat(int(actor.task.Fd()), "ns/"+name, unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		file := os.NewFile(uintptr(fd), "original-vmm-actual-namespace")
		info, statErr := file.Stat()
		closeErr := file.Close()
		if statErr != nil || closeErr != nil {
			return errors.Join(statErr, closeErr)
		}
		if expected[index] == nil || !os.SameFile(info, expected[index]) {
			return vmmModeFailureV1("the received namespace evidence differs from the actual native actor")
		}
	}
	return actor.live()
}
