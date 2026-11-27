//go:build linux

package process

import (
	"errors"

	"github.com/iamlakshya/simstim/internal/supply"
	"golang.org/x/sys/unix"
)

// This handle originates only in the actual cmd.Start error path, before the
// sole original request writer has sent any byte. It is cleanup-only: no
// started Cmd, init receipt or filesystem loan can be issued from it.
func (s *sandbox) closeUnstartedCreatorV3() error {
	if s.partialCreator == nil {
		return nil
	}
	if s.cmd != nil {
		return setupError("a started creator cannot use prepared-only retirement", nil)
	}
	fd := int(s.partialCreator.Fd())
	if s.partialCreatorFacts == nil {
		poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		count, err := unix.Poll(poll, 0)
		if err != nil || count != 1 || poll[0].Revents&unix.POLLIN == 0 || poll[0].Revents&(unix.POLLERR|unix.POLLNVAL) != 0 {
			return setupError("the unstarted native creator has not positively exited", err)
		}
		if err := unix.PidfdSendSignal(fd, 0, nil, 0); !errors.Is(err, unix.ESRCH) {
			return setupError("the unstarted original birth handle is not an exited pidfd", err)
		}
		facts := supply.CloseFileOnceV1(s.partialCreator)
		s.partialCreatorFacts = &facts
	}
	facts := s.partialCreatorFacts
	if !facts.Retired || facts.Unknown != nil {
		return setupError("the original partial creator has unknown native retirement", facts.Unknown)
	}
	s.result.Err = errors.Join(s.result.Err, facts.Diagnostic)
	return nil
}
