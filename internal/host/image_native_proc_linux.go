//go:build linux

package host

import (
	"errors"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
)

func imageGuardianExitedV1(process *os.File) (bool, error) {
	if process == nil {
		return false, imageNativeFailureV1("the original guardian creation descriptor is missing")
	}
	fd := int(process.Fd())
	if fd < 0 || int64(fd) > 1<<31-1 {
		return false, imageNativeFailureV1("the original guardian creation descriptor is invalid")
	}
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	for {
		_, err := unix.Poll(fds, 0)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return false, err
		}
		if fds[0].Revents & ^int16(unix.POLLIN|unix.POLLHUP) != 0 {
			return false, imageNativeFailureV1("the original guardian exit observation is uncertain")
		}
		return fds[0].Revents&unix.POLLIN != 0, nil
	}
}
func imageGuardianPIDV1(process *os.File) (int, error) {
	data, err := os.ReadFile("/proc/self/fdinfo/" + strconv.Itoa(int(process.Fd())))
	if err != nil {
		return 0, err
	}
	pid := 0
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || key != "Pid" {
			continue
		}
		fields := strings.Fields(value)
		if pid != 0 || len(fields) != 1 {
			return 0, imageNativeFailureV1("the actual guardian pidfd identity is ambiguous")
		}
		pid, err = strconv.Atoi(fields[0])
		if err != nil || pid <= 0 || strconv.Itoa(pid) != fields[0] {
			return 0, imageNativeFailureV1("the actual guardian pidfd no longer names its original live task")
		}
	}
	if pid == 0 {
		return 0, imageNativeFailureV1("the supplied guardian descriptor is not an actual pidfd")
	}
	return pid, nil
}
func imageGuardianTaskV1(task *os.File, pid int) (uint64, int, error) {
	data, err := imageGuardianProcReadV1(task, "stat")
	if err != nil {
		return 0, 0, err
	}
	text := string(data)
	open := strings.Index(text, " (")
	close := strings.LastIndex(text, ") ")
	if open <= 0 || close <= open || text[:open] != strconv.Itoa(pid) {
		return 0, 0, imageNativeFailureV1("the original guardian native task identity changed")
	}
	fields := strings.Fields(text[close+2:])
	if len(fields) < 20 || fields[0] == "Z" || fields[0] == "X" {
		return 0, 0, imageNativeFailureV1("the original guardian ended before its native proof")
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, err
	}
	birth, err := strconv.ParseUint(fields[19], 10, 64)
	return birth, parent, err
}
func imageGuardianMembershipV1(task *os.File) (string, error) {
	data, err := imageGuardianProcReadV1(task, "cgroup")
	if err != nil {
		return "", err
	}
	group := ""
	for _, line := range strings.Split(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, "0::"); ok {
			if group != "" || !path.IsAbs(value) || path.Clean(value) != value || strings.ContainsRune(value, 0) {
				return "", imageNativeFailureV1("the actual guardian cgroup membership is ambiguous")
			}
			group = value
		}
	}
	if group == "" {
		return "", imageNativeFailureV1("the original guardian has no actual cgroup v2 membership")
	}
	return group, nil
}
func imageGuardianProcReadV1(task *os.File, name string) ([]byte, error) {
	fd, err := unix.Openat(int(task.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "original-image-guardian-proc")
	defer file.Close()
	var fs unix.Statfs_t
	if err := unix.Fstatfs(fd, &fs); err != nil || fs.Type != unix.PROC_SUPER_MAGIC {
		return nil, errors.Join(err, imageNativeFailureV1("the guardian evidence is not native procfs"))
	}
	data, err := io.ReadAll(io.LimitReader(file, 8193))
	if err != nil {
		return nil, err
	}
	if len(data) > 8192 {
		return nil, imageNativeFailureV1("the original guardian proc evidence exceeds its native bound")
	}
	return data, nil
}
