//go:build linux

package host

import (
	"errors"
	"os"
)

func rememberForkCloseV1(previous, current error) error {
	if current == nil || errors.Is(previous, current) {
		return previous
	}
	return errors.Join(previous, current)
}

func (child *forkChildV1) closeResourcesV1() error {
	if child == nil || child.operation == nil && child.reservation == nil {
		return forkFailureV1("fork_destination", "the original fork cleanup lost its actual child")
	}
	if child.closed || child.closeErr != nil {
		return child.closeErr
	}
	if child.operation != nil {
		if err := child.operation.CloseWithError(); err != nil {
			child.closeErr = err
			return err
		}
	}
	if child.reservation != nil {
		if child.reservation.launched {
			// Launched files belong to the real session. Only these original
			// reservation descriptors close; no path, actor or file is removed.
			roots := []*os.Root{child.reservation.dir, child.reservation.parent}
			child.closeErr = child.retirement.closeRootsV4(roots)
			if native := child.retirement.checkRootsV4(roots); native != nil {
				child.closeErr = rememberForkCloseV1(child.closeErr, native)
				return child.closeErr
			}
		} else if err := closeNewNoRoomReservationV1(child.reservation); err != nil {
			// The actual never-launched producer reports its first removal/close
			// failure. Keep that same reservation; do not replay via reopened paths.
			child.closeErr = err
			return err
		}
	}
	child.closed = true
	return child.closeErr
}
