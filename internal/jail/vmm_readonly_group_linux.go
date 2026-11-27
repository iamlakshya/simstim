//go:build linux

package jail

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/supply"
	"golang.org/x/sys/unix"
)

// Only the actual original S captures this kernel group from its own task.
// No unit name, saved row, PID argument, callback or path setter can issue it.
// It is retained separately from the artifact final receipt until physical join.
type readonlyVMMGroupV1 struct {
	group, self                   *os.File
	pid                           int
	birth                         uint64
	selfAttempted, groupAttempted bool
	selfClose, groupClose         supply.FileCloseFactsV1
}

func currentReadonlyGroupV1() (_ *os.File, returned error) {
	data, err := os.ReadFile("/proc/thread-self/cgroup")
	if err != nil {
		return nil, err
	}
	if len(data) > 4096 {
		return nil, vmmModeFailureV1("the original creator cgroup is oversized")
	}
	line := strings.TrimSuffix(string(data), "\n")
	path, ok := strings.CutPrefix(line, "0::")
	if !ok || strings.ContainsRune(path, '\n') || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return nil, vmmModeFailureV1("the original creator has no private native cgroup-v2 path")
	}
	root, err := unix.Open("/sys/fs/cgroup", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer func() { returned = errors.Join(returned, unix.Close(root)) }()
	var fs unix.Statfs_t
	if err := unix.Fstatfs(root, &fs); err != nil {
		return nil, err
	}
	if fs.Type != unix.CGROUP2_SUPER_MAGIC {
		return nil, vmmModeFailureV1("the original creator cgroup root is not native cgroup-v2")
	}
	fd, err := unix.Openat2(root, strings.TrimPrefix(path, "/"), &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "original-readonly-vmm-cgroup"), nil
}

func readonlyGroupLeafV1(group *os.File, name string, limit int64) (_ []byte, returned error) {
	fd, err := unix.Openat2(int(group.Fd()), name, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "original-readonly-vmm-group-leaf")
	defer func() { returned = errors.Join(returned, file.Close()) }()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.Join(err, vmmModeFailureV1("the original cgroup leaf is unavailable or oversized"))
	}
	return data, nil
}

func captureReadonlyVMMGroupV1(maximum int64) (_ *readonlyVMMGroupV1, returned error) {
	if maximum <= 0 {
		return nil, vmmModeFailureV1("the original read-only VMM has no admitted memory cap")
	}
	group, err := currentReadonlyGroupV1()
	if err != nil {
		if group != nil {
			return &readonlyVMMGroupV1{group: group, pid: os.Getpid()}, err
		}
		return nil, err
	}
	self, err := openVMMTaskV1(os.Getpid())
	if err != nil {
		return failedReadonlyGroupV1(&readonlyVMMGroupV1{group: group, pid: os.Getpid()}, err)
	}
	state, err := readVMMTaskV1(self)
	if err != nil {
		return failedReadonlyGroupV1(&readonlyVMMGroupV1{group: group, self: self, pid: os.Getpid()}, err)
	}
	owner := &readonlyVMMGroupV1{group: group, self: self, pid: os.Getpid(), birth: state.birth}
	for _, entry := range []struct{ name, want string }{{"memory.max", strconv.FormatInt(maximum, 10)}, {"memory.swap.max", "0"}, {"cgroup.type", "domain"}} {
		data, err := readonlyGroupLeafV1(group, entry.name, 256)
		if err != nil || strings.TrimSpace(string(data)) != entry.want {
			return failedReadonlyGroupV1(owner, errors.Join(err, vmmModeFailureV1("the original read-only VMM cgroup lost its native memory/domain boundary")))
		}
	}
	if err := owner.closedExceptCreatorV1(); err != nil {
		return failedReadonlyGroupV1(owner, err)
	}
	return owner, nil
}

// Positive kernel population, held group identity and actual S task birth are
// required. A timeout, unit property or old invocation row is never closure.
func (group *readonlyVMMGroupV1) closedExceptCreatorV1() (returned error) {
	if group == nil || group.group == nil || group.self == nil || os.Getpid() != group.pid {
		return vmmModeFailureV1("the original read-only VMM cgroup owner is missing")
	}
	state, err := readVMMTaskV1(group.self)
	if err != nil || state.birth != group.birth {
		return errors.Join(err, vmmModeFailureV1("the actual original creator task changed lifetime"))
	}
	current, err := currentReadonlyGroupV1()
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, current.Close()) }()
	before, err := group.group.Stat()
	if err != nil {
		return err
	}
	now, err := current.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(before, now) {
		return vmmModeFailureV1("the actual creator changed its original native cgroup")
	}
	data, err := readonlyGroupLeafV1(group.group, "cgroup.stat", 4096)
	if err != nil {
		return err
	}
	found := false
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "nr_descendants" {
			if found || fields[1] != "0" {
				return vmmModeFailureV1("the original read-only VMM cgroup has unknown descendants")
			}
			found = true
		}
	}
	if !found {
		return vmmModeFailureV1("the original cgroup has no native descendant count")
	}
	data, err = readonlyGroupLeafV1(group.group, "cgroup.procs", 64<<10)
	if err != nil {
		return err
	}
	pids := strings.Fields(string(data))
	if len(pids) != 1 || pids[0] != strconv.Itoa(group.pid) {
		return vmmModeFailureV1("the original VMM cgroup still contains another physical actor")
	}
	state, err = readVMMTaskV1(group.self)
	if err != nil || state.birth != group.birth {
		return errors.Join(err, vmmModeFailureV1("the original creator ended during cgroup closure proof"))
	}
	return nil
}

func (group *readonlyVMMGroupV1) Close() error {
	if group == nil {
		return nil
	}
	if group.self != nil {
		if !group.selfAttempted {
			group.selfAttempted = true
			group.selfClose = supply.CloseFileOnceV1(group.self)
			if err := errors.Join(group.selfClose.Diagnostic, group.selfClose.Unknown); err != nil {
				return err
			}
		}
		if group.selfClose.Unknown != nil {
			return group.selfClose.Unknown
		}
	}
	if group.group != nil {
		if !group.groupAttempted {
			group.groupAttempted = true
			group.groupClose = supply.CloseFileOnceV1(group.group)
			if err := errors.Join(group.groupClose.Diagnostic, group.groupClose.Unknown); err != nil {
				return err
			}
		}
		if group.groupClose.Unknown != nil {
			return group.groupClose.Unknown
		}
	}
	return nil
}
func failedReadonlyGroupV1(owner *readonlyVMMGroupV1, failure error) (*readonlyVMMGroupV1, error) {
	if cleanup := owner.Close(); cleanup != nil {
		return owner, errors.Join(failure, cleanup)
	}
	return nil, failure
}
