//go:build linux

package host

import (
	"context"
	"errors"
	"github.com/godbus/dbus/v5"
	"github.com/iamlakshya/simstim/internal/proto"
	"sync"
	"time"
)

// auxiliaryOperationV1 owns real native vacancy, captured artifacts and a
// freshly admitted accounting reservation. It is not yet a boot receipt: only
// its separate original created activation can consume it. No ordinary Run,
// Life, namespace or client state is projected or published here.
type auxiliaryOperationV1 struct {
	ctx         context.Context
	operation   *SessionOperation
	vacancy     *auxiliaryVacancyV1
	artifacts   *auxiliaryArtifactsV1
	publication *runtimePublication
	reservation AuxiliaryReservationV1
	activation  *nativeUnitActivationV1
	creation    *dbus.Call
	job         dbus.ObjectPath
	line        *StartupLineV1
	loanSent    bool
	transport   *auxiliaryTransportV1
	settled     bool
	closeMu     sync.Mutex
	hello       proto.Hello
	mu          sync.Mutex
	published   bool
	dispatched  bool
	closed      bool
	once        sync.Once
	closeErr    error
}

func prepareAuxiliaryOperationV1(ctx context.Context, operation *SessionOperation) (result *auxiliaryOperationV1, returned error) {
	vacancy, err := captureAuxiliaryVacancyV1(ctx, operation)
	if err != nil {
		return nil, err
	}
	return prepareAuxiliaryOperationFromVacancyV1(ctx, operation, vacancy)
}

func prepareAuxiliaryOperationFromVacancyV1(ctx context.Context, operation *SessionOperation, vacancy *auxiliaryVacancyV1) (result *auxiliaryOperationV1, returned error) {
	if vacancy == nil || vacancy.operation != operation {
		return nil, auxiliaryUnavailableV1("the auxiliary preparation has another original vacancy")
	}
	var err error
	owner := &auxiliaryOperationV1{operation: operation, vacancy: vacancy}
	complete := false
	defer func() {
		if !complete {
			closeErr := owner.closeUnstarted()
			returned = errors.Join(returned, closeErr)
			if closeErr != nil {
				result = owner
			}
		}
	}()
	owner.artifacts, err = captureAuxiliaryArtifactsV1(ctx, vacancy)
	if err != nil {
		return nil, err
	}
	token, err := footprintNonce()
	if err != nil {
		return nil, err
	}
	owner.reservation = AuxiliaryReservationV1{Version: 1, Token: token, BootID: vacancy.boot, Since: time.Now().UTC(), Declared: owner.artifacts.spec.MemoryMax}
	if err := validateAuxiliaryReservationV1Data(&owner.reservation); err != nil {
		return nil, err
	}
	if err := bindPurgeAuxiliaryOperationV1(ctx, owner); err != nil {
		return nil, err
	}
	if err := checkPurgeAuxiliaryAdmissionV1(ctx); err != nil {
		return nil, err
	}
	ctx = context.WithValue(auxiliaryLifetimeContextV1{Context: operation.lifetime, values: ctx}, auxiliaryAccountingOriginKeyV1{}, owner)
	owner.ctx = ctx
	owner.publication, err = beginRuntimePublicationKind(ctx, "auxiliary", nil, nil)
	if err != nil {
		return nil, err
	}
	if owner.publication.owner.domain() != owner.artifacts.domain {
		return nil, auxiliaryUnavailableV1("the original auxiliary placement domain changed before reservation")
	}
	if err := owner.publishReservation(ctx); err != nil {
		return nil, err
	}
	complete = true
	return owner, nil
}
func (owner *auxiliaryOperationV1) publishReservation(ctx context.Context) error {
	if owner == nil || owner.vacancy == nil || owner.artifacts == nil || owner.publication == nil {
		return auxiliaryUnavailableV1("auxiliary reservation has no original native issuer")
	}
	if err := owner.vacancy.Check(ctx); err != nil {
		return err
	}
	return owner.publication.gate(ctx, func() error {
		if _, err := admitAuxiliaryMemoryLockedV1(ctx, owner.operation, &owner.reservation); err != nil {
			return err
		}
		if err := owner.writeReservationV1(ctx, nil, &owner.reservation); err != nil {
			return err
		}
		return nil
	})
}

// closeUnstarted is only for the actual pre-dispatch owner. A dispatched
// native activation MUST use its own physical join/absence settlement; this
// method refuses rather than interpreting cancellation or metadata as exit.
func (owner *auxiliaryOperationV1) closeUnstarted() error {
	if owner == nil {
		return nil
	}
	owner.mu.Lock()
	dispatched, published := owner.dispatched, owner.published
	owner.mu.Unlock()
	if dispatched {
		return auxiliaryUnavailableV1("the original auxiliary activation must physically join before its reservation closes")
	}
	if published {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(owner.ctx), 5*time.Second)
		defer cancel()
		if err := owner.vacancy.Check(ctx); err != nil {
			return err
		}
		if err := withAuxiliaryPlacementV1(ctx, func(*placementControl) error { return owner.writeReservationV1(ctx, &owner.reservation, nil) }, false); err != nil {
			return err
		}
		owner.mu.Lock()
		owner.published = false
		owner.mu.Unlock()
	}
	if owner.publication != nil {
		owner.publication.stopWatching()
	}
	owner.once.Do(func() {
		if owner.artifacts != nil {
			owner.closeErr = errors.Join(owner.closeErr, owner.artifacts.Close())
		}
		if owner.publication != nil {
			owner.closeErr = errors.Join(owner.closeErr, owner.publication.close())
		}
		if owner.vacancy != nil {
			owner.closeErr = errors.Join(owner.closeErr, owner.vacancy.Close())
		}
		owner.mu.Lock()
		owner.closed = true
		owner.mu.Unlock()
	})
	return owner.closeErr
}
