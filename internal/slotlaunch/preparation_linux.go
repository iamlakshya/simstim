//go:build linux

package slotlaunch

import (
	"errors"
	"os"
)

// recordVMPreparation runs under the session, store and slot locks, after
// proving the preceding allocation stopped. The protected record establishes
// caller ownership only. It supplies no native process, account or slot proof.
func (b *backend) recordVMPreparation(record *sessionRecord, slotUID int, digests []string) error {
	if record.Session == "" || record.UID != b.caller.uid || record.GID != b.caller.gid {
		return refused("the preparation has no original caller-owned session")
	}
	var existing sessionRecord
	err := b.readJSON("sessions/"+record.Session+".json", &existing)
	if errors.Is(err, os.ErrNotExist) {
		if err := b.confirmSessionAbsent(record.Session); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if existing.Session != record.Session || existing.UID != b.caller.uid || existing.GID != b.caller.gid {
		return refused("the existing preparation belongs to another login account")
	}
	record.Slot, record.SlotUID = nil, slotUID
	if len(record.Digests) == 0 {
		record.Digests = digests
	}
	return b.writeJSON("sessions/"+record.Session+".json", record)
}
