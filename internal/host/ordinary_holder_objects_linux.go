//go:build linux

package host

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
	"golang.org/x/sys/unix"
)

// The lock PID may be the original detached launcher. Linux keeps that PID
// in its FLOCK observation after inheritance. A departed locker may render as -1. The live current code/sender
// is authenticated separately; this check observes only the SAME locked OFD.
func checkOrdinaryHolderFlockV1(file *os.File) error {
	if file == nil {
		return sessionLeaseError("the ordinary holder has no original descriptor")
	}
	var native unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &native); err != nil {
		return err
	}
	raw, err := ordinaryFDInfoV1(file)
	if err != nil {
		return err
	}
	identity := fmt.Sprintf("%02x:%02x:%d", unix.Major(uint64(native.Dev)), unix.Minor(uint64(native.Dev)), native.Ino)
	found := false
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "lock:" {
			continue
		}
		if len(fields) != 9 || found || fields[1] != "1:" || fields[2] != "FLOCK" || fields[3] != "ADVISORY" || fields[4] != "WRITE" || fields[6] != identity || fields[7] != "0" || fields[8] != "EOF" {
			return sessionLeaseError("the ordinary holder descriptor has another original native lock role")
		}
		pid, err := strconv.Atoi(fields[5])
		if err != nil || pid == 0 || pid < -1 {
			return sessionLeaseError("the ordinary holder lock has no original native owner")
		}
		found = true
	}
	if !found {
		return sessionLeaseError("the ordinary holder supplied an unlocked or independently reopened description")
	}
	return nil
}

func ordinaryFDInfoV1(file *os.File) (_ []byte, returned error) {
	info, err := os.Open(fmt.Sprintf("/proc/self/fdinfo/%d", file.Fd()))
	if err != nil {
		return nil, err
	}
	defer func() { returned = errors.Join(returned, info.Close()) }()
	var filesystem unix.Statfs_t
	if err := unix.Fstatfs(int(info.Fd()), &filesystem); err != nil || filesystem.Type != unix.PROC_SUPER_MAGIC {
		return nil, errors.Join(sessionLeaseError("the ordinary descriptor observation is not kernel procfs"), err)
	}
	raw, err := io.ReadAll(io.LimitReader(info, 4097))
	if err != nil || len(raw) > 4096 {
		return nil, errors.Join(sessionLeaseError("the ordinary native descriptor observation exceeds its bound"), err)
	}
	return raw, nil
}

// Adoption never tries to acquire or unlock a descriptor. A new same-inode
// open cannot become authority through this path.
func adoptOrdinaryHolderFileV1(ctx context.Context, primary string, file *os.File) (_ *sessionFileLease, returned error) {
	root, err := cachefs.OpenPrivateRoot(paths.SessionDir(primary), false)
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	complete := false
	defer func() {
		if !complete {
			returned = errors.Join(returned, file.Close(), root.Close())
		}
	}()
	rootRef, err := cachefs.CaptureRoot(root)
	if err != nil {
		return nil, err
	}
	fileRef, err := cachefs.CaptureFile(root, holderLockFile, file, 0o600)
	if err != nil {
		return nil, err
	}
	if err := checkOrdinaryHolderFlockV1(file); err != nil {
		return nil, err
	}
	lease := &sessionFileLease{root: root, file: file, rootRef: rootRef, fileRef: fileRef, id: primary, path: paths.SessionDir(primary), name: holderLockFile, borrowed: true}
	if err := lease.check(ctx); err != nil {
		return nil, err
	}
	complete = true
	return lease, nil
}

func ordinaryPidfdExitedV1(file *os.File) error {
	if file == nil {
		return sessionLeaseError("the ordinary main creation descriptor ended")
	}
	// pidfd_send_signal validates the native descriptor kind. ESRCH means
	// the same original process ended; it never targets a replacement PID.
	if err := unix.PidfdSendSignal(int(file.Fd()), 0, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
		return err
	}
	polls := []unix.PollFd{{Fd: int32(file.Fd()), Events: unix.POLLIN}}
	count, err := unix.Poll(polls, 0)
	if err != nil || count != 1 || polls[0].Revents&unix.POLLIN == 0 {
		return errors.Join(sessionLeaseError("the original ordinary main has no positive native exit"), err)
	}
	return nil
}

func ordinaryJoinLatchV1(file *os.File, completed bool) error {
	if file == nil {
		return sessionLeaseError("the ordinary join has no original native completion object")
	}
	raw, err := ordinaryFDInfoV1(file)
	if err != nil {
		return err
	}
	count, id := "", ""
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		switch fields[0] {
		case "eventfd-count:":
			if count != "" {
				return sessionLeaseError("the ordinary completion object has duplicate kernel state")
			}
			count = fields[1]
		case "eventfd-id:":
			if id != "" {
				return sessionLeaseError("the ordinary completion object has duplicate kernel identity")
			}
			id = fields[1]
		}
	}
	value, err := strconv.ParseUint(count, 16, 64)
	if err != nil || id == "" || completed && value != 1 || !completed && value != 0 {
		return errors.Join(sessionLeaseError("the actual ordinary join has no exact native completion state"), err)
	}
	return nil
}
