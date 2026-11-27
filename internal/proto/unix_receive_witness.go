package proto

import (
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
)

// This is an ephemeral actual receive witness, NOT a token/registry/DTO store.
// Only the canonical native receiver sets it. Ordinary caller fields cannot
// create one, and copies share one consume/close lifetime.
type unixReceiveWitnessV1 struct {
	mu                 sync.Mutex
	connection         *net.UnixConn
	connectionIdentity string
	credentials        UnixCredentialsV1
	hasCredentials     bool
	files              []*os.File
	identities         []os.FileInfo
	consumed, closed   bool
}

func newUnixReceiveWitnessV1(conn *net.UnixConn, control *UnixControlV1) error {
	identity, err := unixReceiveConnectionIdentityV1(conn)
	if err != nil {
		return err
	}
	witness := &unixReceiveWitnessV1{connection: conn, connectionIdentity: identity, files: append([]*os.File(nil), control.Files...)}
	if control.Credentials != nil {
		witness.credentials = *control.Credentials
		witness.hasCredentials = true
	}
	for _, file := range witness.files {
		if file == nil {
			return fmt.Errorf("native receive materialised a missing descriptor")
		}
		info, err := file.Stat()
		if err != nil {
			return err
		}
		witness.identities = append(witness.identities, info)
	}
	control.received = witness
	return nil
}

// CheckReceivedV1 proves ONLY unchanged provenance from the original live
// native receive. The actual consumer must still check frame, roles, creator
// ancestry, original operation and code. It grants no execution authority.
func (control *UnixControlV1) CheckReceivedV1(conn *net.UnixConn) error {
	if control == nil || control.received == nil {
		return fmt.Errorf("control has no actual native receive provenance")
	}
	witness := control.received
	witness.mu.Lock()
	defer witness.mu.Unlock()
	return witness.check(control, conn)
}

func (witness *unixReceiveWitnessV1) check(control *UnixControlV1, conn *net.UnixConn) error {
	if witness.closed || witness.consumed || conn == nil || conn != witness.connection {
		return fmt.Errorf("native receive provenance is closed, consumed or has another connection")
	}
	identity, err := unixReceiveConnectionIdentityV1(conn)
	if err != nil {
		return err
	}
	if identity != witness.connectionIdentity {
		return fmt.Errorf("native receive connection changed its actual identity")
	}
	if witness.hasCredentials != (control.Credentials != nil) || witness.hasCredentials && *control.Credentials != witness.credentials {
		return fmt.Errorf("native receive credentials were reconstructed or changed")
	}
	if len(control.Files) != len(witness.files) {
		return fmt.Errorf("native receive descriptor roles changed")
	}
	for i, file := range witness.files {
		if file == nil || control.Files[i] != file {
			return fmt.Errorf("native receive substituted a materialised descriptor")
		}
		info, err := file.Stat()
		if err != nil {
			return err
		}
		if !os.SameFile(info, witness.identities[i]) {
			return fmt.Errorf("native receive descriptor changed native identity")
		}
	}
	return nil
}

// DetachFilesV1 explicitly transfers this SAME materialised vector once.
// Hand-built legacy fixture controls retain their ordinary ownership-transfer
// semantics, but never gain a native receive witness by calling this method.
func (control *UnixControlV1) DetachFilesV1() ([]*os.File, error) {
	if control == nil {
		return nil, fmt.Errorf("native control is missing")
	}
	if control.received == nil {
		files := control.Files
		control.Files = nil
		return files, nil
	}
	witness := control.received
	witness.mu.Lock()
	defer witness.mu.Unlock()
	if err := witness.check(control, witness.connection); err != nil {
		return nil, err
	}
	witness.consumed = true
	files := control.Files
	control.Files = nil
	return files, nil
}

func (witness *unixReceiveWitnessV1) close() error {
	witness.mu.Lock()
	defer witness.mu.Unlock()
	if witness.closed {
		return nil
	}
	witness.closed = true
	if witness.consumed {
		witness.files = nil
		witness.identities = nil
		return nil
	}
	var failure error
	for _, file := range witness.files {
		if file != nil {
			failure = errors.Join(failure, file.Close())
		}
	}
	witness.files = nil
	witness.identities = nil
	return failure
}
