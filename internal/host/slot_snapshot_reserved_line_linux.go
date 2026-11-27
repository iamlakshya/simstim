//go:build linux

package host

import (
	"errors"
	"net"
	"os"

	"github.com/iamlakshya/simstim/internal/proto"
)

// V2 is a distinct exact reserved role. Ordinary startup, V1 snapshots and
// read/check operands retain their own bounds and cannot enter this reader.
func receiveSlotSnapshotReservationAncillaryV2(conn net.Conn, data []byte) (int, []*os.File, *startupCredentials, error) {
	native, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, nil, nil, sessionLeaseError("the reserved Snapshot loan lost its original Unix channel")
	}
	n, control, err := proto.ReceiveUnixControlV1(native, data, proto.NativeCheckpointReservationRoleCountV2)
	var credentials *startupCredentials
	if control.Credentials != nil {
		credentials = &startupCredentials{pid: control.Credentials.PID, uid: control.Credentials.UID, gid: control.Credentials.GID}
	}
	return n, control.Files, credentials, err
}

func (line *StartupLineV1) armSlotSnapshotReservationV2() error {
	line.mu.Lock()
	defer line.mu.Unlock()
	if line.closed || line.reading || line.snapshotLoan || line.snapshotReservation || line.slotStartupPending != nil || len(line.files) != 0 || line.loanFiles != 1 {
		return sessionLeaseError("the reserved Snapshot reader is not at its original empty boundary")
	}
	line.snapshotReservation = true
	return nil
}

func (line *StartupLineV1) takeSlotSnapshotReservationV2() ([]*os.File, startupCredentials, error) {
	line.mu.Lock()
	defer line.mu.Unlock()
	if line.closed || line.reading || !line.snapshotReservation || line.snapshotLoan || line.slotStartupPending != nil || len(line.files) != proto.NativeCheckpointReservationRoleCountV2 || line.cred == nil || line.controlAt != line.start {
		return nil, startupCredentials{}, errors.Join(sessionLeaseError("the reserved Snapshot response has no exact native role loan"), line.closeAncillaryLocked())
	}
	files, sender := line.files, *line.cred
	line.files = nil
	return files, sender, nil
}

func (line *StartupLineV1) finishSlotSnapshotReservationV2() error {
	line.mu.Lock()
	defer line.mu.Unlock()
	if line.reading {
		return sessionLeaseError("the reserved Snapshot reader has not joined")
	}
	line.snapshotReservation = false
	if len(line.files) != 0 {
		return errors.Join(sessionLeaseError("the reserved Snapshot response left unclaimed native roles"), line.closeAncillaryLocked())
	}
	return nil
}
