//go:build linux

package host

import (
	"encoding/json"
	"errors"
	"net"
	"os"

	"github.com/iamlakshya/simstim/internal/proto"
)

func (line *StartupLineV1) WriteNativeHelloV1(hello proto.Hello) (returned error) {
	identity, err := currentNativeHelperIdentityV1()
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, identity.Close()) }()
	connection, ok := line.conn.(*net.UnixConn)
	if !ok {
		return sessionLeaseError("the native hello has no original Unix connection")
	}
	raw, err := json.Marshal(hello)
	if err != nil {
		return err
	}
	line.writeMu.Lock()
	defer line.writeMu.Unlock()
	return proto.SendUnixFrameV1(connection, proto.THello, raw, []*os.File{identity.executable, identity.user, identity.cgroup})
}

// AcceptNativeHelloV1 binds the current native sender to this actual socket
// and retained native process handle. Later binding to the original creator's
// pidfd is mandatory on the outside side before any private descriptor send.
func (line *StartupLineV1) AcceptNativeHelloV1(frame proto.Frame) error {
	return line.acceptNativeHelperIdentityV1(frame, proto.THello)
}
func (line *StartupLineV1) acceptNativeHelperIdentityV1(frame proto.Frame, expectedType proto.Type) error {
	identity, sender, err := line.takeNativeHelperIdentityFrameV1(frame, expectedType)
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		if !complete {
			_ = identity.Close()
		}
	}()
	_, expected, err := mappedExecutable()
	if err != nil {
		return err
	}
	if err := identity.Check(expected); err != nil {
		return err
	}
	peer, err := pinUnverifiedNativeHelperPeerV1(line.conn)
	if err != nil {
		return err
	}
	if err := peer.checkNativeSender(sender); err != nil {
		return errors.Join(err, peer.Close())
	}
	identity.expected = expected
	peer.identity = identity
	if err := peer.checkLive(); err != nil {
		return errors.Join(err, peer.Close())
	}
	line.mu.Lock()
	defer line.mu.Unlock()
	if line.closed || line.nativePeer != nil {
		return errors.Join(sessionLeaseError("the original native hello ended during its handoff"), peer.Close())
	}
	line.nativePeer = peer
	complete = true
	return nil
}
func (line *StartupLineV1) bindOriginalNativePeerV1(original *nativeStartupPeerV1) error {
	if line == nil || original == nil {
		return sessionLeaseError("the native hello has no original creation owner")
	}
	line.mu.Lock()
	peer := line.nativePeer
	line.mu.Unlock()
	if peer == nil || peer.identity == nil || peer.pid != original.pid || peer.uid != original.uid {
		return sessionLeaseError("the native hello differs from the actual original creation process")
	}
	if err := errors.Join(peer.checkLive(), original.checkLive()); err != nil {
		return err
	}
	original.mu.Lock()
	defer original.mu.Unlock()
	if original.closed || original.identity != nil && original.identity != peer.identity {
		return sessionLeaseError("the original creation identity was already replaced")
	}
	original.identity = peer.identity
	return nil
}

// This helper transfers only exact public frame DATA ownership. It issues no
// code, creator, namespace, original operation or private-loan receipt.
func (line *StartupLineV1) takeNativeHelperIdentityFrameV1(frame proto.Frame, expectedType proto.Type) (*nativeHelperIdentityV1, startupCredentials, error) {
	if line == nil || frame.Type != expectedType {
		return nil, startupCredentials{}, sessionLeaseError("the native helper identity arrived outside its hello")
	}
	line.mu.Lock()
	if line.closed || line.reading || line.loanFiles != 3 || len(line.files) != 3 || line.cred == nil || line.controlAt != line.start || line.nativePeer != nil {
		err := line.closeAncillaryLocked()
		line.mu.Unlock()
		return nil, startupCredentials{}, errors.Join(sessionLeaseError("the native hello has no exact original code and namespace roles"), err)
	}
	files, sender := line.files, *line.cred
	line.files = nil // Retain current same-frame SCM metadata for its proof checks.
	line.mu.Unlock()
	return &nativeHelperIdentityV1{executable: files[0], user: files[1], cgroup: files[2]}, sender, nil
}
