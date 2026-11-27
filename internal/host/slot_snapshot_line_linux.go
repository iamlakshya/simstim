//go:build linux

package host

import (
	"errors"
	"net"
	"os"

	"github.com/iamlakshya/simstim/internal/proto"
)

// This fixed role vector belongs only to the original Snapshot receiver.
// Ordinary startup's one role and auxiliary's three roles remain unchanged.
func receiveSlotSnapshotAncillaryV1(conn net.Conn, data []byte) (int, []*os.File, *startupCredentials, error) {
	native, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, nil, nil, sessionLeaseError("the Snapshot loan lost its original Unix channel")
	}
	n, control, err := proto.ReceiveUnixControlV1(native, data, proto.NativeCheckpointRoleCountV1)
	var credentials *startupCredentials
	if control.Credentials != nil {
		credentials = &startupCredentials{pid: control.Credentials.PID, uid: control.Credentials.UID, gid: control.Credentials.GID}
	}
	return n, control.Files, credentials, err
}

func (line *StartupLineV1) armSlotSnapshotV1() error {
	line.mu.Lock()
	defer line.mu.Unlock()
	if line.closed || line.reading || line.snapshotLoan || line.snapshotReservation || line.slotStartupPending != nil || len(line.files) != 0 || line.loanFiles != 1 {
		return sessionLeaseError("the original Snapshot reader is not at an empty closed-purpose boundary")
	}
	line.snapshotLoan = true
	return nil
}

func (line *StartupLineV1) takeSlotSnapshotV1() ([]*os.File, startupCredentials, error) {
	line.mu.Lock()
	defer line.mu.Unlock()
	if line.closed || line.reading || !line.snapshotLoan || line.snapshotReservation || line.slotStartupPending != nil || len(line.files) != proto.NativeCheckpointRoleCountV1 || line.cred == nil || line.controlAt != line.start {
		return nil, startupCredentials{}, errors.Join(sessionLeaseError("the original Snapshot response has no exact native role loan"), line.closeAncillaryLocked())
	}
	files, sender := line.files, *line.cred
	line.files = nil
	return files, sender, nil
}

func (line *StartupLineV1) finishSlotSnapshotV1() error {
	line.mu.Lock()
	defer line.mu.Unlock()
	if line.reading {
		return sessionLeaseError("the original Snapshot reader has not joined")
	}
	line.snapshotLoan = false
	if len(line.files) != 0 {
		return errors.Join(sessionLeaseError("the Snapshot response left unclaimed native roles"), line.closeAncillaryLocked())
	}
	return nil
}
