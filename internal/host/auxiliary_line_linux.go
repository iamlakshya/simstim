//go:build linux

package host

import (
	"encoding/json"
	"errors"
	"net"
	"os"

	"github.com/iamlakshya/simstim/internal/proto"
)

// newAuxiliaryStartupLineV1 selects exactly three closed native lease roles:
// original owning OP, original owning holder lock and original saved disk.
// Ordinary startup keeps
// its exact one-role limit. No wire field or caller option selects this bound.
func newAuxiliaryStartupLineV1(connection net.Conn) (*StartupLineV1, error) {
	line, err := NewStartupLineV1(connection)
	if err != nil {
		return nil, err
	}
	line.loanFiles = 3
	return line, nil
}
func (line *StartupLineV1) takeAuxiliaryFilesV1() ([]*os.File, startupCredentials, error) {
	line.mu.Lock()
	defer line.mu.Unlock()
	if line.closed || line.reading || line.loanFiles != 3 || len(line.files) != 3 || line.cred == nil || line.controlAt != line.start {
		return nil, startupCredentials{}, errors.Join(sessionLeaseError("auxiliary startup has no exact original OP and holder loan"), line.closeAncillaryLocked())
	}
	files, credentials := line.files, *line.cred
	line.files, line.cred = nil, nil
	return files, credentials, nil
}
func (line *StartupLineV1) writeAuxiliaryLoanV1(value any, operation, holder, disk *os.File) error {
	if line == nil || operation == nil || holder == nil || disk == nil {
		return auxiliaryUnavailableV1("the auxiliary loan has no original native lease pair")
	}
	connection, ok := line.conn.(*net.UnixConn)
	if !ok {
		return auxiliaryUnavailableV1("the auxiliary loan lost its original native Unix line")
	}
	line.writeMu.Lock()
	defer line.writeMu.Unlock()
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return proto.SendUnixFrameV1(connection, proto.TRequest, raw, []*os.File{operation, holder, disk})
}
