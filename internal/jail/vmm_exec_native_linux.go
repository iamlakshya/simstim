//go:build linux

package jail

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	vmmStageOuterV1   byte = 1
	vmmStageInitV1    byte = 2
	vmmStageExecV1    byte = 3
	vmmStageRunnerV1  byte = 4
	vmmModeEnterV1    byte = 1
	vmmModeCreateV1   byte = 2
	vmmModeNoFutureV1 byte = 3
)

type vmmNativeMessageV1 struct {
	kind, stage byte
	localPID    uint32
}

func (message vmmNativeMessageV1) bytes() []byte {
	data := make([]byte, 16)
	copy(data, "VMMMODE1")
	data[8], data[9] = message.kind, message.stage
	binary.LittleEndian.PutUint32(data[12:], message.localPID)
	return data
}
func readVMMNativeMessageV1(data []byte) (vmmNativeMessageV1, error) {
	var message vmmNativeMessageV1
	if len(data) != 16 || string(data[:8]) != "VMMMODE1" || data[10] != 0 || data[11] != 0 {
		return message, vmmModeFailureV1("the original native creation record is malformed")
	}
	message = vmmNativeMessageV1{data[8], data[9], binary.LittleEndian.Uint32(data[12:])}
	if message.kind < vmmModeEnterV1 || message.kind > vmmModeNoFutureV1 || message.stage < vmmStageOuterV1 || message.stage > vmmStageRunnerV1 || message.localPID == 0 {
		return message, vmmModeFailureV1("the native creation record has another closed phase")
	}
	return message, nil
}

func vmmPIDFDIdentityV1(file *os.File) (int, []int, error) {
	if file == nil {
		return 0, nil, vmmModeFailureV1("the actual creation descriptor is missing")
	}
	if _, err := unix.FcntlInt(file.Fd(), unix.F_GETFD, 0); err != nil {
		return 0, nil, err
	}
	data, err := os.ReadFile(vmmFDInfoPathV1(file))
	if err != nil {
		return 0, nil, err
	}
	if len(data) > 4096 {
		return 0, nil, vmmModeFailureV1("the native creation fdinfo exceeds its bound")
	}
	pid := 0
	var namespace []int
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields := strings.Fields(value)
		if key == "Pid" {
			if pid != 0 || len(fields) != 1 {
				return 0, nil, vmmModeFailureV1("the actual creation descriptor has ambiguous identity")
			}
			pid, err = strconv.Atoi(fields[0])
			if err != nil || pid <= 0 || strconv.Itoa(pid) != fields[0] {
				return 0, nil, vmmModeFailureV1("the actual creation descriptor no longer names a live task")
			}
		}
		if key == "NSpid" {
			if namespace != nil || len(fields) == 0 {
				return 0, nil, vmmModeFailureV1("the actual creation descriptor has ambiguous namespace ancestry")
			}
			for _, field := range fields {
				n, e := strconv.Atoi(field)
				if e != nil || n <= 0 || strconv.Itoa(n) != field {
					return 0, nil, vmmModeFailureV1("the actual creation namespace identity is malformed")
				}
				namespace = append(namespace, n)
			}
		}
	}
	if pid <= 0 || len(namespace) == 0 || namespace[0] != pid {
		return 0, nil, vmmModeFailureV1("the supplied file is not an actual native creation pidfd")
	}
	return pid, namespace, nil
}

func captureVMMActorV1(process *os.File) (*vmmExecActorV1, error) {
	pid, _, err := vmmPIDFDIdentityV1(process)
	if err != nil {
		return nil, err
	}
	ready, err := ownedPIDFDReady(int(process.Fd()), 0)
	if err != nil || ready {
		return nil, errors.Join(err, vmmModeFailureV1("the actual creation child ended before its original adoption"))
	}
	task, err := openVMMTaskV1(pid)
	if err != nil {
		return nil, err
	}
	state, err := readVMMTaskV1(task)
	if err != nil || state.pid != pid {
		task.Close()
		return nil, errors.Join(err, vmmModeFailureV1("the native task differs from the actual creation descriptor"))
	}
	return &vmmExecActorV1{process: process, task: task, pid: pid, birth: state.birth}, nil
}

type vmmTaskV1 struct {
	pid, parent int
	birth       uint64
}

func openVMMTaskV1(pid int) (*os.File, error) {
	fd, err := unix.Open("/proc/"+strconv.Itoa(pid), unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	var fs unix.Statfs_t
	if err := unix.Fstatfs(fd, &fs); err != nil || fs.Type != unix.PROC_SUPER_MAGIC {
		unix.Close(fd)
		return nil, errors.Join(err, vmmModeFailureV1("the creation task is not on the original native proc filesystem"))
	}
	return os.NewFile(uintptr(fd), "original-vmm-native-task"), nil
}
func readVMMTaskV1(task *os.File) (vmmTaskV1, error) {
	var state vmmTaskV1
	fd, err := unix.Openat(int(task.Fd()), "stat", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return state, err
	}
	file := os.NewFile(uintptr(fd), "original-vmm-task-state")
	data, err := io.ReadAll(io.LimitReader(file, 8193))
	err = errors.Join(err, file.Close())
	if err != nil {
		return state, err
	}
	return parseVMMTaskV1(data)
}
func parseVMMTaskV1(data []byte) (vmmTaskV1, error) {
	var state vmmTaskV1
	text := string(data)
	open := strings.Index(text, " (")
	close := strings.LastIndex(text, ") ")
	if len(data) > 8192 || open < 1 || close <= open {
		return state, vmmModeFailureV1("the original task state is malformed")
	}
	var err error
	state.pid, err = strconv.Atoi(text[:open])
	if err != nil || state.pid <= 0 || strconv.Itoa(state.pid) != text[:open] {
		return state, vmmModeFailureV1("the original task identity is malformed")
	}
	fields := strings.Fields(text[close+2:])
	if len(fields) < 20 || len(fields[0]) != 1 || fields[0] == "Z" || fields[0] == "X" {
		return state, vmmModeFailureV1("the original task ended before its native proof")
	}
	state.parent, err = strconv.Atoi(fields[1])
	if err != nil || state.parent < 0 {
		return state, vmmModeFailureV1("the original native ancestry is malformed")
	}
	state.birth, err = strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return state, vmmModeFailureV1("the original native task birth is malformed")
	}
	return state, nil
}
func (actor *vmmExecActorV1) live() error {
	if actor == nil || actor.process == nil {
		return vmmModeFailureV1("the original native actor is missing")
	}
	ready, err := ownedPIDFDReady(int(actor.process.Fd()), 0)
	if err != nil || ready {
		return errors.Join(err, vmmModeFailureV1("the original native actor no longer remains live"))
	}
	state, err := readVMMTaskV1(actor.task)
	if err != nil {
		return err
	}
	if state.pid != actor.pid || state.birth != actor.birth {
		return vmmModeFailureV1("the original actor changed its native task identity")
	}
	return nil
}
