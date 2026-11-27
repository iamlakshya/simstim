//go:build linux

package slotlaunch

import (
	"bytes"
	"encoding/json"
	"errors"
	"golang.org/x/sys/unix"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
)

func receiveNative(connection *net.UnixConn) (Response, []*os.File, *unix.Ucred, error) {
	var response Response
	var files []*os.File
	var credential *unix.Ucred
	var data []byte
	raw, err := connection.SyscallConn()
	if err != nil {
		return response, nil, nil, err
	}
	for len(data) <= RequestMax {
		buffer := make([]byte, RequestMax+1)
		control := make([]byte, unix.CmsgSpace(16*4)+unix.CmsgSpace(unix.SizeofUcred))
		var n, cn, flags int
		var receiveErr error
		err = raw.Read(func(fd uintptr) bool {
			n, cn, flags, _, receiveErr = unix.Recvmsg(int(fd), buffer, control, unix.MSG_CMSG_CLOEXEC)
			return !errors.Is(receiveErr, unix.EAGAIN)
		})
		if err != nil || receiveErr != nil || n == 0 || flags&(unix.MSG_CTRUNC|unix.MSG_TRUNC) != 0 {
			closeReceivedRights(control[:cn])
			return response, files, credential, refused("the native root handoff was incomplete or truncated")
		}
		if len(data) > RequestMax || n > RequestMax-len(data) {
			closeReceivedRights(control[:cn])
			return response, files, credential, refused("the native root response exceeds its bound")
		}
		messages, err := unix.ParseSocketControlMessage(control[:cn])
		if err != nil {
			return response, files, credential, err
		}
		for _, message := range messages {
			if message.Header.Level == unix.SOL_SOCKET && message.Header.Type == unix.SCM_RIGHTS {
				descriptors, err := unix.ParseUnixRights(&message)
				if err != nil {
					return response, files, credential, err
				}
				for _, fd := range descriptors {
					files = append(files, os.NewFile(uintptr(fd), "actual-root-slot-role"))
				}
			}
		}
		for _, message := range messages {
			if message.Header.Level != unix.SOL_SOCKET {
				return response, files, credential, refused("the native handoff has an unknown control level")
			}
			switch message.Header.Type {
			case unix.SCM_CREDENTIALS:
				next, err := unix.ParseUnixCredentials(&message)
				if err != nil {
					return response, files, credential, err
				}
				if credential != nil && *credential != *next {
					return response, files, credential, refused("the root source changed senders")
				}
				credential = next
			case unix.SCM_RIGHTS:
				// Captured in the first pass so every refusal closes them.
			default:
				return response, files, credential, refused("the native handoff has an unknown control role")
			}
		}
		data = append(data, buffer[:n]...)
		if bytes.IndexByte(data, '\n') >= 0 {
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&response) != nil || decoder.Decode(new(any)) != io.EOF {
				return response, files, credential, refused("the native root response is not one bounded object")
			}
			return response, files, credential, nil
		}
	}
	return response, files, credential, refused("the native root response exceeds its bound")
}

func closeReceivedRights(control []byte) {
	messages, err := unix.ParseSocketControlMessage(control)
	if err != nil {
		return
	}
	for _, message := range messages {
		if message.Header.Level == unix.SOL_SOCKET && message.Header.Type == unix.SCM_RIGHTS {
			fds, err := unix.ParseUnixRights(&message)
			if err == nil {
				for _, fd := range fds {
					_ = unix.Close(fd)
				}
			}
		}
	}
}

func nativePID(file *os.File) (int, error) {
	if !isNativePIDFD(file) {
		return 0, refused("the native process handle is not a kernel pidfd")
	}
	raw, err := os.ReadFile("/proc/self/fdinfo/" + strconv.Itoa(int(file.Fd())))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "Pid:\t") {
			pid, err := strconv.Atoi(strings.TrimPrefix(line, "Pid:\t"))
			if err == nil && pid > 0 {
				return pid, nil
			}
		}
	}
	return 0, refused("the native process handle is not an identifiable live pidfd")
}
func pidLive(file *os.File) bool {
	if !isNativePIDFD(file) {
		return false
	}
	items := []unix.PollFd{{Fd: int32(file.Fd()), Events: unix.POLLIN}}
	n, err := unix.Poll(items, 0)
	return err == nil && n == 0
}

func pidExited(file *os.File) bool {
	if !isNativePIDFD(file) {
		return false
	}
	items := []unix.PollFd{{Fd: int32(file.Fd()), Events: unix.POLLIN}}
	n, err := unix.Poll(items, 0)
	return err == nil && n == 1 && items[0].Revents&unix.POLLIN != 0 && items[0].Revents&(unix.POLLERR|unix.POLLNVAL) == 0
}

func isNativePIDFD(file *os.File) bool {
	if file == nil {
		return false
	}
	name, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(int(file.Fd())))
	return err == nil && name == "anon_inode:[pidfd]"
}
