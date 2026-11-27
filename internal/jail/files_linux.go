package jail

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// captureFile takes an owned copy while Control keeps the caller's handle
// alive. A concurrent File.Close either wins before Control or cannot
// remove the capability until its copy is held. Neither check uses Fd.
func captureFile(file *os.File, name, hint string) (*os.File, error) {
	if file == nil {
		return nil, failure("a jail's "+name+" is closed", hint)
	}
	raw, err := file.SyscallConn()
	copyFD := -1
	var descriptorErr error
	if err == nil {
		err = raw.Control(func(fd uintptr) {
			_, descriptorErr = unix.FcntlInt(fd, unix.F_GETFD, 0)
			if descriptorErr == nil {
				copyFD, descriptorErr = unix.FcntlInt(fd, unix.F_DUPFD_CLOEXEC, 3)
			}
		})
	}
	err = errors.Join(err, descriptorErr)
	if err != nil {
		if copyFD >= 0 {
			_ = unix.Close(copyFD)
		}
		if errors.Is(err, os.ErrClosed) || errors.Is(err, os.ErrInvalid) || errors.Is(err, unix.EBADF) {
			return nil, failure("a jail's "+name+" is closed", hint)
		}
		return nil, stepFailed("cannot keep the jail's "+name, hint+" Check the open-file limit.", err)
	}
	return os.NewFile(uintptr(copyFD), file.Name()), nil
}

// blockingPipe changes only the command's private pipe endpoint. The
// caller's opposite endpoint has a different open-file description, so
// its nonblocking flag and Go poller remain as the caller received them.
func blockingPipe(file *os.File, name string) error {
	raw, err := file.SyscallConn()
	var descriptorErr error
	if err == nil {
		err = raw.Control(func(fd uintptr) {
			var flags int
			flags, descriptorErr = unix.FcntlInt(fd, unix.F_GETFL, 0)
			if descriptorErr == nil && flags&unix.O_NONBLOCK != 0 {
				_, descriptorErr = unix.FcntlInt(fd, unix.F_SETFL, flags&^unix.O_NONBLOCK)
			}
		})
	}
	if err := errors.Join(err, descriptorErr); err != nil {
		return stepFailed("cannot make the jail's "+name+" pipe blocking",
			"Create the stream with "+name+"Pipe, and try again.", err)
	}
	return nil
}

func closeFiles(files []*os.File) {
	for _, file := range files {
		_ = file.Close()
	}
}

// checkStageFiles is a second check of expected inherited slots. It
// cannot identify a slot that startup already refilled. Start's owned
// captures prevent that hole, before any stage process is created.
func checkStageFiles(files int) error {
	if files < 0 || files > int(^uint(0)>>1)-3 {
		return failure("the jail's inherited file count is invalid", "Start jails with jail.Command, not by hand.")
	}
	for fd := range 3 + files {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err != nil {
			return stepFailed("a jail stage's inherited file is closed", "Start jails with jail.Command and keep its files open until Start.", err)
		}
	}
	return nil
}
