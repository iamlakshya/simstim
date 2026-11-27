//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// noneWarmHolderV1 retains the actual current None sender's received held OFD.
// It supplies only this original warm/N5 scope, never whole-stop authority.
type noneWarmHolderV1 struct {
	self             *noneWarmHolderV1
	owner            *StoppedSessionV1
	operation        *SessionOperation
	invocation       *nativeStopInvocationV1
	holder           *sessionFileLease
	file             *os.File
	boot, generation string
}

func (owner *StoppedSessionV1) receiveNativeNoneHolderProofV1(ctx context.Context, id uint64, inspect string) (returned error) {
	if owner == nil || ctx == nil || inspect != proto.NativeResumeInspectV1 || owner.operation == nil || owner.operation.native == nil || owner.operation.native.borrowed || !owner.operation.Valid(owner.operation.Primary()) || owner.invocation == nil || owner.line == nil || !NoneSupervisorShapeV1(&owner.source) || len(owner.source.VMNeedV1) != 0 || owner.source.RuntimeManifest != nil || !auxiliaryBootIDV1(owner.boot) || owner.boot != BootID() || owner.source.Run.Generation == "" || owner.state.Generation != owner.source.Run.Generation || owner.state.Need != startupDigest(nil) || owner.state.Runtime != startupDigest(nil) || owner.noneHolder != nil || owner.holder != nil {
		return sessionLeaseError("the None holder proof has no original live outside scope")
	}
	if err := errors.Join(CheckCaller(), owner.invocation.Check(ctx), owner.operation.native.check(ctx), ctx.Err()); err != nil {
		return err
	}
	frame, err := owner.line.ReadFrame()
	if err != nil {
		return err
	}
	var response proto.Response
	if frame.Type != proto.TResponse || !api.ExactJSONFields(frame.Payload, response) || json.Unmarshal(frame.Payload, &response) != nil || response.ID != id || !response.OK {
		return errors.Join(sessionLeaseError("the original None sender supplied no exact holder proof"), owner.line.RejectAncillary())
	}
	var proof struct {
		Version int    `json:"v"`
		Primary string `json:"primary"`
	}
	if !api.ExactJSONFields(response.Result, proof) || json.Unmarshal(response.Result, &proof) != nil || proof.Version != 1 || proof.Primary != owner.operation.Primary() {
		return errors.Join(sessionLeaseError("the original None holder proof changed its primary"), owner.line.RejectAncillary())
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
	if err := errors.Join(owner.invocation.peer.checkNativeSender(sender), owner.invocation.Check(ctx), observeNoneHolderV1(file), ctx.Err()); err != nil {
		return err
	}
	root, err := owner.operation.native.root.OpenRoot(".")
	if err != nil {
		return err
	}
	rootKept := false
	defer func() {
		if !rootKept {
			returned = errors.Join(returned, root.Close())
		}
	}()
	rootRef, err := cachefs.CaptureRoot(root)
	if err != nil {
		return err
	}
	ref, err := cachefs.CaptureFile(root, holderLockFile, file, 0600)
	if err != nil {
		return err
	}
	lease := &sessionFileLease{root: root, file: file, rootRef: rootRef, fileRef: ref, id: owner.operation.Primary(), path: paths.SessionDir(owner.operation.Primary()), name: holderLockFile, borrowed: true}
	if err := errors.Join(owner.operation.native.rootRef.CheckRoot(root), lease.check(ctx), observeNoneHolderV1(file), owner.invocation.Check(ctx), ctx.Err()); err != nil {
		return err
	}
	bound := &noneWarmHolderV1{owner: owner, operation: owner.operation, invocation: owner.invocation, holder: lease, file: file, boot: owner.boot, generation: owner.state.Generation}
	bound.self = bound
	owner.holder, owner.noneHolder = lease, bound
	adopted, rootKept = true, true
	return nil
}

// The live producer is checked separately by its original native invocation.
// After actual N5 pidfd completion this only retains/checks its SAME held OFD;
// it never reconstructs a live/walled source from the saved state.
func (bound *noneWarmHolderV1) checkDescriptionV1(ctx context.Context, owner *StoppedSessionV1) error {
	if bound == nil || bound.self != bound || ctx == nil || owner == nil || bound.owner != owner || owner.noneHolder != bound || bound.operation != owner.operation || bound.operation == nil || bound.operation.native == nil || bound.operation.native.borrowed || !bound.operation.Valid(bound.operation.Primary()) || bound.invocation != owner.invocation || bound.invocation == nil || bound.holder != owner.holder || bound.holder == nil || bound.file != bound.holder.file || bound.file == nil || bound.generation == "" || bound.generation != owner.state.Generation || bound.boot != owner.boot || !auxiliaryBootIDV1(bound.boot) || bound.boot != BootID() || !NoneSupervisorShapeV1(&owner.source) {
		return sessionLeaseError("the original None holder description ended, was copied or changed its source")
	}
	return errors.Join(bound.holder.check(ctx), bound.operation.native.rootRef.CheckRoot(bound.holder.root), observeNoneHolderV1(bound.file), ctx.Err())
}
