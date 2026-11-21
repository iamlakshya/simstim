//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// WriteNativeStopProofV1 supplies only this actual committed supervisor's
// native code/namespace identity and already-owned holder description. The
// caller serialises it on its ONE original outside control connection.
func (owner *OwnedSupervisorLease) WriteNativeStopProofV1(ctx context.Context, connection net.Conn, response proto.Response) (returned error) {
	if owner == nil || ctx == nil {
		return sessionLeaseError("the native stop proof has no original supervisor")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.native == nil || !owner.Valid(owner.native.id) || owner.startup == nil || !owner.startup.ready || owner.native.borrowed {
		return sessionLeaseError("the native stop proof has no original managed holder owner")
	}
	native, ok := connection.(*net.UnixConn)
	if !ok {
		return sessionLeaseError("the native stop proof has no original Unix connection")
	}
	identity, err := currentNativeHelperIdentityV1()
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, identity.Close()) }()
	holder, err := duplicateAuxiliaryHolderV1(ctx, owner.native)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, holder.Close()) }()
	raw, err := json.Marshal(response)
	if err != nil {
		return err
	}
	if err := proto.SendUnixFrameV1(native, proto.TResponse, raw, []*os.File{identity.executable, identity.user, identity.cgroup}); err != nil {
		return err
	}
	// The second exact frame carries one distinct PRIVATE role. It cannot be
	// confused with a public namespace vector or manufactured by JSON fields.
	proof, err := json.Marshal(struct {
		Version int    `json:"v"`
		Primary string `json:"primary"`
	}{1, owner.native.id})
	if err != nil {
		return err
	}
	raw, err = json.Marshal(proto.Response{ID: response.ID, OK: true, Result: proof})
	if err != nil {
		return err
	}
	return proto.SendUnixFrameV1(native, proto.TResponse, raw, []*os.File{holder})
}
func (line *StartupLineV1) acceptNativeStopIdentityV1(frame proto.Frame) error {
	return line.acceptNativeHelperIdentityV1(frame, proto.TResponse)
}
func (owner *StoppedSessionV1) receiveNativeHolderProofV1(id uint64) (returned error) {
	frame, err := owner.line.ReadFrame()
	if err != nil {
		return err
	}
	var response proto.Response
	if frame.Type != proto.TResponse || !api.ExactJSONFields(frame.Payload, response) || json.Unmarshal(frame.Payload, &response) != nil || response.ID != id || !response.OK {
		return errors.Join(auxiliaryUnavailableV1("the original supervisor supplied no native holder proof"), owner.line.RejectAncillary())
	}
	var proof struct {
		Version int    `json:"v"`
		Primary string `json:"primary"`
	}
	if !api.ExactJSONFields(response.Result, proof) || json.Unmarshal(response.Result, &proof) != nil || proof.Version != 1 || proof.Primary != owner.operation.Primary() {
		return errors.Join(auxiliaryUnavailableV1("the original holder proof changed its primary"), owner.line.RejectAncillary())
	}
	file, sender, err := owner.line.takeLoanFile()
	if err != nil {
		return err
	}
	adopted := false
	defer func() {
		if !adopted {
			returned = errors.Join(returned, file.Close())
		}
	}()
	if err := errors.Join(owner.invocation.peer.checkNativeSender(sender), owner.invocation.Check(context.Background()), checkBorrowedFlockV1(file, sender)); err != nil {
		return err
	}
	held, err := adoptSupervisorFileLease(context.Background(), owner.operation.Primary(), file)
	if err != nil {
		return err
	}
	if err := owner.operation.native.rootRef.CheckRoot(held.root); err != nil {
		return errors.Join(err, held.Close())
	}
	owner.holder = held
	adopted = true
	return nil
}
