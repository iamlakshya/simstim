//go:build linux

package host

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// checkBorrowedFlockV1 observes the lock on this exact kernel open file
// description. Linux's show_fd_locks filters by fl_file and fl_owner==filp;
// an independently reopened same-inode FD cannot carry this observation.
// Never attempt LOCK_EX or LOCK_UN here: either could create/release authority.
func checkBorrowedFlockV1(file *os.File, sender startupCredentials) (failure error) {
	if file == nil || sender.pid <= 0 || sender.uid != uint32(os.Getuid()) {
		return sessionLeaseError("the operation loan has no original native lock sender")
	}
	actual, err := file.Stat()
	if err != nil {
		return err
	}
	stat, ok := actual.Sys().(*unix.Stat_t)
	if !ok {
		return sessionLeaseError("the operation loan has no native file identity")
	}
	info, err := os.Open(fmt.Sprintf("/proc/self/fdinfo/%d", file.Fd()))
	if err != nil {
		return err
	}
	defer func() { failure = errors.Join(failure, info.Close()) }()
	var filesystem unix.Statfs_t
	if err := unix.Fstatfs(int(info.Fd()), &filesystem); err != nil || filesystem.Type != unix.PROC_SUPER_MAGIC {
		return errors.Join(sessionLeaseError("the operation lock observation is not kernel procfs"), err)
	}
	raw, err := io.ReadAll(io.LimitReader(info, 4097))
	if err != nil || len(raw) > 4096 {
		return errors.Join(sessionLeaseError("the operation lock observation is invalid or exceeds its bound"), err)
	}
	identity := fmt.Sprintf("%02x:%02x:%d", unix.Major(uint64(stat.Dev)), unix.Minor(uint64(stat.Dev)), stat.Ino)
	found := false
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "lock:" {
			continue
		}
		if found || len(fields) != 9 || fields[1] != "1:" || fields[2] != "FLOCK" || fields[3] != "ADVISORY" || fields[4] != "WRITE" ||
			fields[5] != strconv.Itoa(sender.pid) || fields[6] != identity || fields[7] != "0" || fields[8] != "EOF" {
			return sessionLeaseError("the received file is not the original sender's already-held exclusive OP description")
		}
		found = true
	}
	if !found {
		return sessionLeaseError("the received same-name file carries no original held OP description")
	}
	return nil
}
