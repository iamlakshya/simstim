//go:build linux

package jail

import "golang.org/x/sys/unix"

// retainVMMExecStageV1 runs after the final generic ruleset is enforced. It
// preserves only these exact three native roles across this trusted exec.
func retainVMMExecStageV1(c *setup, argv *[]string) (returned error) {
	if c.VMMExecV1 == nil {
		return nil
	}
	if err := checkVMMLayoutV1(c); err != nil {
		return err
	}
	sources := []int{c.VMMExecV1.LeaseFD, c.VMMExecV1.WriterFD, c.VMMExecV1.ControlFD}
	var copies, reserved []int
	defer func() {
		for _, fd := range copies {
			unix.Close(fd)
		}
		if returned != nil {
			for _, fd := range reserved {
				unix.Close(fd)
			}
		}
	}()
	for _, fd := range sources {
		copyFD, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 64)
		if err != nil {
			return err
		}
		copies = append(copies, copyFD)
	}
	for index, copyFD := range copies {
		target := 12 + index
		if err := closeVMMConsumedSlotV1(c, target, copies); err != nil {
			return err
		}
		// This atomically reserves an empty target. Never check-then-Dup3 over
		// a possible Go poller, runtime descriptor or unrelated inherited file.
		fd, err := unix.FcntlInt(uintptr(copyFD), unix.F_DUPFD_CLOEXEC, target)
		if err != nil {
			return err
		}
		if fd != target {
			unix.Close(fd)
			return vmmModeFailureV1("the closed VMM stage target is occupied by an unknown descriptor")
		}
		reserved = append(reserved, fd)
	}
	for _, fd := range reserved {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, 0); err != nil {
			return err
		}
	}
	*argv = append(append([]string(nil), (*argv)...), vmmRunnerMarkerV1)
	return nil
}

// After ruleset enforcement, these raw pin capabilities have no remaining
// purpose. Close a target only if its exact captured native identity agrees.
// Unknown occupants are left untouched for the atomic reservation to refuse.
func closeVMMConsumedSlotV1(c *setup, target int, copies []int) error {
	for _, bound := range c.Binds {
		if bound.FD == target {
			return closeVMMCapturedPinV1(target, bound.Dev, bound.Ino)
		}
	}
	for _, rule := range c.Rules {
		if rule.FD == target && rule.FD > 0 {
			return closeVMMCapturedPinV1(target, rule.Dev, rule.Ino)
		}
	}
	for index, original := range []int{c.VMMExecV1.LeaseFD, c.VMMExecV1.WriterFD, c.VMMExecV1.ControlFD} {
		if target != original {
			continue
		}
		same, err := sameReadonlyOFDV1(target, copies[index])
		if err != nil {
			return err
		}
		if !same {
			return vmmModeFailureV1("a consumed private VMM alias changed its original OFD")
		}
		return unix.Close(target)
	}
	return nil
}
func closeVMMCapturedPinV1(fd int, dev, ino uint64) error {
	var actual unix.Stat_t
	if err := unix.Fstat(fd, &actual); err != nil {
		return err
	}
	if uint64(actual.Dev) != dev || uint64(actual.Ino) != ino {
		return vmmModeFailureV1("a consumed VMM pin changed before its exact private slot was released")
	}
	return unix.Close(fd)
}
