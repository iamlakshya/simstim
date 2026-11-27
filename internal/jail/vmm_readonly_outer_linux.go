//go:build linux

package jail

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// A stays the SAME actual outside lifetime parent. It never consumes queued
// C/B records after its terminal ACK. EOF/HUP only triggers exact pidfd kill;
// positive C wait and S's native physical proof remain separate obligations.
func finishReadonlyOuterV1(c *setup, command *exec.Cmd, creation *os.File, extra []*os.File, syncWriter *os.File, signals <-chan os.Signal) int {
	if c == nil || c.VMMExecV1 == nil || !c.VMMExecV1.ReadonlyInit || creation == nil || command.Process == nil {
		return stageFailed(vmmModeFailureV1("the read-only outer lost its actual init creation"))
	}
	if err := sendVMMStageV1(c, vmmModeNoFutureV1, vmmStageOuterV1, nil); err != nil {
		command.Process.Kill()
		command.Wait()
		return stageFailed(err)
	}
	var held, status *os.File
	for _, file := range extra {
		if file == nil {
			continue
		}
		if int(file.Fd()) == c.VMMExecV1.ControlFD {
			held = file
			continue
		}
		if int(file.Fd()) == c.StatusFD {
			status = file
			continue
		}
		if err := file.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			command.Process.Kill()
			command.Wait()
			return stageFailed(err)
		}
	}
	if held == nil {
		return stageFailed(vmmModeFailureV1("the original outer lost its retained lifetime endpoint"))
	}
	stop, done := make(chan struct{}), make(chan error, 1)
	go func() { done <- watchReadonlyOutsideV1(held, creation, stop) }()
	if n, err := syncWriter.Write([]byte{0x72}); err != nil || n != 1 {
		_ = signalReadonlyCreationV1(creation, unix.SIGKILL)
		_ = command.Wait()
		close(stop)
		watchErr := <-done
		return stageFailed(errors.Join(err, watchErr, vmmModeFailureV1("the original outer could not release its terminal reader boundary")))
	}
	relay := startRelay(command.Process.Pid, signals, unix.Kill)
	waitErr := waitChildWithRelay(command.Process.Pid, relay, command.Wait, command.Process.Kill)
	close(stop)
	watchErr := <-done
	if command.ProcessState == nil {
		return stageFailed(errors.Join(waitErr, watchErr, vmmModeFailureV1("the original outer has no actual namespace-init wait result")))
	}
	if status != nil {
		state, ok := command.ProcessState.Sys().(syscall.WaitStatus)
		if !ok {
			return stageFailed(vmmModeFailureV1("the original init wait returned another native shape"))
		}
		if _, err := io.WriteString(status, exitStatus(state)); err != nil {
			return stageFailed(err)
		}
		if err := status.Close(); err != nil {
			return stageFailed(err)
		}
	}
	var exited *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &exited) {
		return stageFailed(errors.Join(waitErr, watchErr))
	}
	if watchErr != nil {
		return stageFailed(watchErr)
	}
	return exitCode(command.ProcessState)
}

func watchReadonlyOutsideV1(endpoint, creation *os.File, stop <-chan struct{}) error {
	raw, err := endpoint.SyscallConn()
	if err != nil {
		return errors.Join(err, signalReadonlyCreationV1(creation, unix.SIGKILL))
	}
	for {
		select {
		case <-stop:
			return nil
		default:
		}
		var events int16
		var native error
		err = raw.Control(func(fd uintptr) {
			poll := []unix.PollFd{{Fd: int32(fd), Events: 0}}
			_, native = unix.Poll(poll, 100)
			events = poll[0].Revents
		})
		if errors.Is(native, unix.EINTR) {
			continue
		}
		if err != nil || native != nil || events&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
			killErr := signalReadonlyCreationV1(creation, unix.SIGKILL)
			return errors.Join(err, native, killErr)
		}
	}
}

func signalReadonlyCreationV1(creation *os.File, signal unix.Signal) error {
	raw, err := creation.SyscallConn()
	if err != nil {
		return err
	}
	var native error
	err = raw.Control(func(fd uintptr) { native = unix.PidfdSendSignal(int(fd), signal, nil, 0) })
	if errors.Is(native, unix.ESRCH) {
		native = nil
	}
	return errors.Join(err, native)
}
