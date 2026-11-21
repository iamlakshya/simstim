//go:build linux

package host

import (
	"errors"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/sys/unix"
)

// custodySelfOnlyV1 reads only the same retained cgroup2 object. It is not
// permission to ignore a runtime's physical join or a privileged slot owner.
func custodySelfOnlyV1(group *os.File) error {
	return custodyOnlyPIDV1(group, os.Getpid())
}

func custodyOnlyPIDV1(group *os.File, pid int) error {
	if group == nil || pid <= 0 {
		return sessionLeaseError("the custody producer has no original unit cgroup")
	}
	var fs unix.Statfs_t
	if err := unix.Fstatfs(int(group.Fd()), &fs); err != nil || fs.Type != unix.CGROUP2_SUPER_MAGIC {
		return errors.Join(sessionLeaseError("custody quiescence has no original cgroup2 object"), err)
	}
	open := func(name string, flags uint64) (*os.File, error) {
		fd, err := unix.Openat2(int(group.Fd()), name, &unix.OpenHow{Flags: flags | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
		if err != nil {
			return nil, err
		}
		return os.NewFile(uintptr(fd), "original-custody-cgroup"), nil
	}
	processes, err := open("cgroup.procs", unix.O_RDONLY)
	if err != nil {
		return err
	}
	raw, readErr := io.ReadAll(io.LimitReader(processes, int64(proto.MaxFrame)+1))
	err = errors.Join(readErr, processes.Close())
	fields := strings.Fields(string(raw))
	if err != nil || len(raw) > proto.MaxFrame || len(fields) != 1 || fields[0] != strconv.Itoa(pid) {
		return errors.Join(sessionLeaseError("another process still belongs to the original custody unit"), err)
	}
	directory, err := open(".", unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return err
	}
	entries, readErr := directory.ReadDir(-1)
	err = errors.Join(readErr, directory.Close())
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return sessionLeaseError("a child cgroup still belongs to the original custody unit")
		}
	}
	return nil
}
