//go:build linux

package jail

import (
	"errors"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

func (actor *vmmExecActorV1) checkReadonlyFDV1(slot int, expected *os.File) error {
	if err := actor.live(); err != nil {
		return err
	}
	fd, err := unix.Openat(int(actor.task.Fd()), "fd/"+strconv.Itoa(slot), unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var got, want unix.Stat_t
	if err := errors.Join(unix.Fstat(fd, &got), unix.Fstat(int(expected.Fd()), &want)); err != nil {
		return err
	}
	if got.Dev != want.Dev || got.Ino != want.Ino || got.Mode != want.Mode || got.Size != want.Size {
		return vmmModeFailureV1("the original live init adopted another actual disk capability")
	}
	return actor.live()
}
