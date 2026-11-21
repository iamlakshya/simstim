//go:build linux

package host

import (
	"context"
	"errors"
	"time"

	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

// OpenAuxiliaryStoppedGuestV1 is the production stopped reader constructor.
// It owns a NEW physical capped native helper under the SAME original OP.
// The first supported slice requires a replaced native boot epoch; current
// boot and unmanaged legacy lifetimes need their original physical stop owner.
func OpenAuxiliaryStoppedGuestV1(ctx context.Context, operation *SessionOperation) (_ StoppedGuest, returned error) {
	owner, err := prepareAuxiliaryOperationV1(ctx, operation)
	if err != nil {
		if owner != nil {
			err = errors.Join(err, owner.Close())
		}
		return nil, err
	}
	return openPreparedAuxiliaryGuestV1(ctx, owner)
}

func openPreparedAuxiliaryGuestV1(ctx context.Context, owner *auxiliaryOperationV1) (_ *auxiliaryOperationV1, returned error) {
	if owner == nil {
		return nil, auxiliaryUnavailableV1("the auxiliary reader has no prepared original source")
	}
	var err error
	complete := false
	defer func() {
		if !complete {
			returned = errors.Join(returned, owner.Close())
		}
	}()
	if err := owner.startAuxiliaryNativeV1(ctx); err != nil {
		return nil, err
	}
	if err := owner.sendAuxiliaryLoanV1(ctx); err != nil {
		return nil, err
	}
	if err := owner.line.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	session := link.New(auxiliaryLineConnectionV1{Conn: owner.line.conn, line: owner.line}, link.Supervisor, link.Options{Opener: auxiliaryOpenersV1})
	owner.transport, err = newAuxiliaryTransportV1(ctx, session)
	if err != nil {
		return nil, errors.Join(err, session.Close())
	}
	complete = true
	return owner, nil
}
func (owner *auxiliaryOperationV1) Manifest() Manifest {
	snapshot, err := owner.operation.LoadContext(owner.ctx)
	if err != nil {
		return Manifest{}
	}
	// Ordinary saved run fields remain exactly the original protected source.
	return CloneManifest(*snapshot)
}
func (owner *auxiliaryOperationV1) Call(ctx context.Context, op string, args, out any) error {
	if op != proto.CtlBundlePrepare && op != proto.CtlBundleCancel && op != proto.CtlPurgeWorkInspectV1 {
		return auxiliaryUnavailableV1("the auxiliary lifetime cannot issue that guest control operation")
	}
	if err := owner.checkAuxiliaryNativeMemoryV1(ctx); err != nil {
		return err
	}
	return owner.transport.Call(ctx, op, args, out)
}
func (owner *auxiliaryOperationV1) Open(ctx context.Context, kind link.Kind, header any) (*link.Stream, error) {
	if err := owner.checkAuxiliaryNativeMemoryV1(ctx); err != nil {
		return nil, err
	}
	return owner.transport.Open(ctx, kind, header)
}
