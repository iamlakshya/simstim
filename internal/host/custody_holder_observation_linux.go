//go:build linux

package host

import (
	"context"
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
)

// adoptManagerHolderObservationV1 is private to the actual native live-proof
// receiver. The same holder OFD can outlive its original locker process. This
// observational copy cannot acquire, unlock or reopen that description.
func (owner *StoppedSessionV1) adoptManagerHolderObservationV1(ctx context.Context, file *os.File) (_ *sessionFileLease, failure error) {
	if owner == nil || owner.invocation == nil || owner.invocation.runtime == nil || owner.operation == nil || !owner.operation.Valid(owner.operation.Primary()) || owner.invocation.peer == nil || owner.invocation.peer.identity == nil {
		return nil, auxiliaryUnavailableV1("holder observation has no actual native manager-runtime producer")
	}
	if err := errors.Join(owner.invocation.Check(ctx), checkCustodyFlockV1(file)); err != nil {
		return nil, err
	}
	root, err := cachefs.OpenPrivateRoot(paths.SessionDir(owner.operation.Primary()), false)
	if err != nil {
		return nil, err
	}
	complete := false
	defer func() {
		if !complete {
			failure = errors.Join(failure, root.Close())
		}
	}()
	rootRef, err := cachefs.CaptureRoot(root)
	if err != nil {
		return nil, err
	}
	if err := owner.operation.native.rootRef.CheckRoot(root); err != nil {
		return nil, err
	}
	fileRef, err := cachefs.CaptureFile(root, holderLockFile, file, 0o600)
	if err != nil {
		return nil, err
	}
	lease := &sessionFileLease{root: root, file: file, rootRef: rootRef, fileRef: fileRef, id: owner.operation.Primary(), path: paths.SessionDir(owner.operation.Primary()), name: holderLockFile, borrowed: true}
	if err := lease.check(ctx); err != nil {
		return nil, err
	}
	complete = true
	return lease, nil
}
