//go:build !windows

package host

import (
	"bytes"
	"errors"
	"io"
	"os"

	"github.com/iamlakshya/simstim/internal/api"
)

const agentEndpointClaimVersion = "agent-endpoint-v1\n"

type agentEndpointClaim struct {
	session string
	socket  string
}

// The version is a liveness contract: this format is written only after
// the claim lock is held, and before the socket can exist. Old empty claims
// were not locked and must never authorise removal of a live old helper.
func recordHostAgentEndpoint(endpoint *hostAgentEndpoint, session, socket string) error {
	if !api.ValidSessionID(session) || !agentEndpointSocketName(socket) {
		return agentSocketError()
	}
	body := []byte(agentEndpointClaimVersion + session + "\n" + socket + "\n")
	n, err := endpoint.claim.WriteAt(body, 0)
	if err != nil || n != len(body) {
		return agentSocketError()
	}
	if err := endpoint.claim.Sync(); err != nil {
		return agentSocketError()
	}
	return nil
}

func readAgentEndpointClaim(file *os.File) (agentEndpointClaim, bool, error) {
	var record agentEndpointClaim
	info, err := file.Stat()
	if err != nil {
		return record, false, agentSocketError()
	}
	// This bound describes our own fixed claim format. Unrecognised local
	// files are preserved; they are not parsed as an alternative authority.
	var body [96]byte
	if info.Size() < 1 || info.Size() > int64(len(body)) {
		return record, false, nil
	}
	n, err := file.ReadAt(body[:info.Size()], 0)
	if err != nil && !errors.Is(err, io.EOF) || int64(n) != info.Size() {
		return record, false, agentSocketError()
	}
	parts := bytes.Split(body[:n], []byte{'\n'})
	if len(parts) != 4 || string(parts[0])+"\n" != agentEndpointClaimVersion || len(parts[3]) != 0 {
		return record, false, nil
	}
	record.session, record.socket = string(parts[1]), string(parts[2])
	if !api.ValidSessionID(record.session) || !agentEndpointSocketName(record.socket) {
		return agentEndpointClaim{}, false, nil
	}
	return record, true, nil
}

func agentEndpointSocketName(name string) bool {
	return len(name) == 19 && name[0] == 'a' && name[17:] == ".s" && agentLowerHex(name[1:17])
}

func agentLowerHex(value string) bool {
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return value != ""
}
