//go:build linux

package microvm

import (
	"fmt"
	"strconv"

	"golang.org/x/sys/unix"
)

// isolateVMMRuntimeOffsetsV1 reopens the final five readonly boot files on
// their checked readonly mounts. Each final OFD starts at zero independently
// of the parent, init and runner. The writable session disk is not reopened.
// The caller completes this before the last 0555 projection receipt.
func isolateVMMRuntimeOffsetsV1() error {
	opened := make([]int, 0, 5)
	defer func() {
		for _, fd := range opened {
			unix.Close(fd)
		}
	}()
	for fd := 3; fd <= 7; fd++ {
		var before, after unix.Stat_t
		if err := unix.Fstat(fd, &before); err != nil {
			return err
		}
		copyFD, err := unix.Open("/proc/self/fd/"+strconv.Itoa(fd), unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if err != nil {
			return fmt.Errorf("isolate final VMM runtime fd %d: %w", fd, err)
		}
		opened = append(opened, copyFD)
		if err := unix.Fstat(copyFD, &after); err != nil {
			return err
		}
		var mount unix.Statfs_t
		if err := unix.Fstatfs(copyFD, &mount); err != nil {
			return err
		}
		if before.Dev != after.Dev || before.Ino != after.Ino || before.Size != after.Size || after.Mode&unix.S_IFMT != unix.S_IFREG || mount.Flags&unix.ST_RDONLY == 0 {
			return fmt.Errorf("final VMM readonly runtime fd %d changed its native source", fd)
		}
	}
	for index, fd := range opened {
		if err := unix.Dup3(fd, 3+index, unix.O_CLOEXEC); err != nil {
			return err
		}
	}
	return nil
}
