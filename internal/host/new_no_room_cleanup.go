package host

import (
	"errors"
	"io"
	"os"
)

// This closes only the SAME never-launched reservation. Unlike the legacy void
// close it reports every read, removal and descriptor error to the New owner.
func closeNewNoRoomReservationV1(reservation *sessionReservation) (failure error) {
	if reservation == nil || reservation.parent == nil || reservation.dir == nil || reservation.created == nil || reservation.launched {
		return sessionLeaseError("the refused New has no original unused reservation")
	}
	defer func() { failure = errors.Join(failure, reservation.dir.Close(), reservation.parent.Close()) }()
	current, err := reservation.parent.Lstat(reservation.id)
	if err != nil || !os.SameFile(reservation.created, current) {
		return errors.Join(sessionLeaseError("the refused reservation changed before cleanup"), err)
	}
	root, err := reservation.dir.Stat(".")
	if err != nil || !os.SameFile(reservation.created, root) {
		return errors.Join(sessionLeaseError("the refused reservation root changed before cleanup"), err)
	}
	directory, err := reservation.dir.Open(".")
	if err != nil {
		return err
	}
	for {
		entries, readErr := directory.ReadDir(128)
		for _, entry := range entries {
			failure = errors.Join(failure, reservation.dir.RemoveAll(entry.Name()))
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			failure = errors.Join(failure, readErr)
			break
		}
	}
	failure = errors.Join(failure, directory.Close())
	if failure != nil {
		return failure
	}
	current, err = reservation.parent.Lstat(reservation.id)
	if err != nil || !os.SameFile(reservation.created, current) {
		return errors.Join(sessionLeaseError("the refused reservation changed during cleanup"), err)
	}
	return reservation.parent.Remove(reservation.id)
}
