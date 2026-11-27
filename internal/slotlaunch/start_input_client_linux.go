//go:build linux

package slotlaunch

import (
	"net"
	"os"

	"golang.org/x/sys/unix"
)

func sendOriginalStartInputsV2(socket *net.UnixConn, attempt *nativeStartAttemptV1, launcher os.FileInfo, input *clientStartInputsV2, parent *os.File) error {
	response, files, credentials, err := receiveNative(socket)
	if err != nil {
		attempt.retainPartial(files)
		return err
	}
	if !response.OK && len(files) == 0 {
		return checkpointRemoteErrorV1(response)
	}
	if !response.OK || len(files) != 2 || credentials == nil || credentials.Uid != 0 {
		attempt.retainPartial(files)
		return refused("the new root input has no actual worker issuer")
	}
	pid, err := nativePID(files[1])
	if err != nil || pid != int(credentials.Pid) || !pidLive(files[1]) {
		attempt.retainPartial(files)
		return refused("the new root input has another actual native sender")
	}
	executed, err := files[0].Stat()
	if err != nil || !singleRegular(executed, 0) || !os.SameFile(launcher, executed) {
		attempt.retainPartial(files)
		return refused("the new root input worker executed another protected producer")
	}
	if err := attempt.captureRootWorker(files[1]); err != nil {
		attempt.retainPartial(files)
		return err
	}
	for _, file := range files {
		file.Close()
	}
	if _, err := socket.Write([]byte{1}); err != nil {
		return err
	}
	var roles []*os.File
	if input.fork != nil {
		roles = []*os.File{input.fork, parent}
	} else {
		roles = append(append([]*os.File(nil), input.reserved...), parent)
	}
	fds := make([]int, 0, len(roles))
	for _, file := range roles {
		if file == nil {
			return refused("an actual original new-Boot input role is absent")
		}
		fds = append(fds, int(file.Fd()))
	}
	raw, err := socket.SyscallConn()
	if err != nil {
		return err
	}
	var sendErr error
	if err := raw.Write(func(fd uintptr) bool {
		sendErr = unix.Sendmsg(int(fd), []byte{1}, unix.UnixRights(fds...), nil, unix.MSG_NOSIGNAL)
		return sendErr != unix.EAGAIN
	}); err != nil {
		return err
	}
	return sendErr
}
