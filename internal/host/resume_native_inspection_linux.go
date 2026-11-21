//go:build linux

package host

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// WarmResumeV1 retains the actual existing native controller. It has no cold
// fallback or permission to fetch/install a new named-key set.
type WarmResumeV1 struct {
	owner    *StoppedSessionV1
	used     bool
	loanSent bool
}

// ColdResumeV1 retains positive original native absence and actual artifacts.
// Its selection data cannot create a native boot; final resume still needs
// the SAME OP, fresh accounting and actual original creation/loan/prepare.
type ColdResumeV1 struct {
	life                sync.Mutex
	operation           *SessionOperation
	vacancy             *auxiliaryVacancyV1
	artifacts           *auxiliaryArtifactsV1
	mu                  sync.Mutex
	closed              bool
	bound               bool
	handed              bool
	startup             *startupLaunchV1
	creation            *os.File
	rewindCreationGroup *os.File
	closeErr            error
	custody             *managerCustodyColdV1
}

// InspectResumeForOperationV1 never stops a live controller. Exactly one
// opaque result is returned. Missing current-boot sockets/old records cannot
// classify a session as cold; unknown lifetimes remain preserved/refused.
func InspectResumeForOperationV1(ctx context.Context, operation *SessionOperation) (*WarmResumeV1, *ColdResumeV1, error) {
	if ctx == nil || operation == nil || operation.native == nil || operation.native.borrowed || !operation.Valid(operation.Primary()) {
		return nil, nil, auxiliaryUnavailableV1("resume inspection has no original owning operation")
	}
	protected, err := operation.LoadContext(ctx)
	if err != nil {
		return nil, nil, err
	}
	if protected.Run.BootID == BootID() {
		owner, err := captureSupportedSupervisorPurposeV1(ctx, operation, proto.NativeResumeInspectV1)
		if err != nil {
			if noHolderSocket(err) {
				custody, captureErr := captureManagerCustodyResumeV1(ctx, operation)
				if captureErr != nil {
					if custody != nil {
						return nil, &ColdResumeV1{operation: operation, custody: custody}, errors.Join(err, captureErr)
					}
					return nil, nil, errors.Join(err, captureErr)
				}
				return nil, &ColdResumeV1{operation: operation, custody: custody}, nil
			}
			return nil, nil, err
		}
		result := &WarmResumeV1{owner: owner}
		if err := result.lendIncomingOperationV1(ctx, operation); err != nil {
			return nil, nil, errors.Join(err, result.Close())
		}
		if err := result.Check(ctx, operation); err != nil {
			return nil, nil, errors.Join(err, result.Close())
		}
		return result, nil, nil
	}
	vacancy, err := captureAuxiliaryVacancyV1(ctx, operation)
	if err != nil {
		return nil, nil, err
	}
	artifacts, err := captureAuxiliaryArtifactsV1(ctx, vacancy)
	if err != nil {
		if artifacts != nil {
			return nil, &ColdResumeV1{operation: operation, vacancy: vacancy, artifacts: artifacts}, err
		}
		closeErr := vacancy.Close()
		if closeErr != nil {
			return nil, &ColdResumeV1{operation: operation, vacancy: vacancy}, errors.Join(err, closeErr)
		}
		return nil, nil, err
	}
	result := &ColdResumeV1{operation: operation, vacancy: vacancy, artifacts: artifacts}
	if err := result.Check(ctx, operation); err != nil {
		return nil, nil, errors.Join(err, result.Close())
	}
	return nil, result, nil
}
func (prepared *WarmResumeV1) Check(ctx context.Context, operation *SessionOperation) error {
	if prepared == nil || prepared.owner == nil || ctx == nil {
		return auxiliaryUnavailableV1("the original warm native controller is absent")
	}
	owner := prepared.owner
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if operation == nil || operation != owner.operation || operation.native == nil || operation.native.borrowed || !operation.Valid(operation.Primary()) || owner.closed || owner.stopSent || owner.settled || owner.holder == nil || owner.invocation == nil || owner.line == nil || owner.origin == nil {
		return auxiliaryUnavailableV1("the original warm native controller ended or changed")
	}
	if err := errors.Join(CheckCaller(), owner.origin.checkIdentity(), owner.holder.check(ctx), operation.native.rootRef.CheckRoot(owner.holder.root), owner.invocation.Check(ctx)); err != nil {
		return err
	}
	if IsNoneSupervisor(&owner.source) {
		if err := owner.noneHolder.checkDescriptionV1(ctx, owner); err != nil {
			return err
		}
	}
	current, err := operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	if BootID() != owner.boot || current.Run.Generation != owner.state.Generation || current.Auxiliary != nil || !bytes.Equal(current.VMNeedV1, owner.source.VMNeedV1) || !reflect.DeepEqual(current.RuntimeManifest, owner.source.RuntimeManifest) {
		return auxiliaryUnavailableV1("the original warm protected controller source changed")
	}
	arguments := proto.NativeStopArgsV1{Version: 1, Primary: operation.Primary(), Nonce: owner.state.Nonce}
	var checked proto.NativeStopStateV1
	if err := owner.nativeStopRequestV1(ctx, 4, proto.NativeResumeCheckV1, arguments, &checked); err != nil {
		return err
	}
	if checked != owner.state {
		return auxiliaryUnavailableV1("the original warm controller continuation changed")
	}
	return ctx.Err()
}
func (prepared *WarmResumeV1) Close() error {
	if prepared == nil || prepared.owner == nil {
		return nil
	}
	owner := prepared.owner
	owner.mu.Lock()
	if prepared.loanSent {
		if err := prepared.releaseIncomingOperationV1(); err != nil {
			owner.mu.Unlock()
			return prepared.retainUnknownWarmV1(err)
		}
	}
	owner.mu.Unlock()
	return owner.Close()
}
func (prepared *ColdResumeV1) Check(ctx context.Context, operation *SessionOperation) error {
	if prepared != nil && prepared.custody != nil {
		return prepared.custody.Check(ctx, operation)
	}
	if prepared == nil || ctx == nil || operation == nil || operation != prepared.operation || prepared.vacancy == nil || prepared.artifacts == nil {
		return auxiliaryUnavailableV1("the original cold native absence/artifact source is absent")
	}
	prepared.mu.Lock()
	defer prepared.mu.Unlock()
	return prepared.checkLockedV1(ctx, operation)
}
func (prepared *ColdResumeV1) checkLockedV1(ctx context.Context, operation *SessionOperation) error {
	if prepared.custody != nil {
		return prepared.custody.Check(ctx, operation)
	}
	if prepared.closed || prepared.vacancy.operation != operation || prepared.artifacts.vacancy != prepared.vacancy {
		return auxiliaryUnavailableV1("the original cold native source ended or changed")
	}
	if prepared.handed {
		return prepared.checkNativeTakeoverLockedV1(ctx, operation)
	}
	if err := prepared.vacancy.Check(ctx); err != nil {
		return err
	}
	return prepared.artifacts.CheckBeforeActivation(ctx)
}
func (prepared *ColdResumeV1) CheckManifest(ctx context.Context, operation *SessionOperation, proposed *Manifest) error {
	if prepared != nil && prepared.custody != nil {
		return prepared.custody.CheckManifest(ctx, operation, proposed)
	}
	if prepared == nil || ctx == nil || operation == nil || operation != prepared.operation || prepared.vacancy == nil || prepared.artifacts == nil {
		return auxiliaryUnavailableV1("the original cold native absence/artifact source is absent")
	}
	prepared.mu.Lock()
	defer prepared.mu.Unlock()
	if err := prepared.checkLockedV1(ctx, operation); err != nil {
		return err
	}
	if proposed == nil || proposed.ID != operation.Primary() || proposed.SandboxOf != "" || proposed.path != "" {
		return auxiliaryUnavailableV1("the cold continuation changed its original protected primary")
	}
	if !bytes.Equal(proposed.VMNeedV1, prepared.vacancy.need) {
		if err := checkAdmittedRestartCurrentV1(operation, proposed); err != nil {
			return err
		}
		return ValidateManifestStart(proposed)
	}
	runtime, err := startupRuntimeBytes(proposed)
	if err != nil {
		return err
	}
	if !bytes.Equal(runtime, prepared.vacancy.runtime) {
		return auxiliaryUnavailableV1("the cold continuation changed its original runtime source")
	}
	return ValidateManifestStart(proposed)
}
func (prepared *ColdResumeV1) Close() error {
	if prepared == nil {
		return nil
	}
	if prepared.custody != nil {
		return prepared.custody.Close()
	}
	prepared.life.Lock()
	defer prepared.life.Unlock()
	prepared.mu.Lock()
	if prepared.closed {
		prepared.mu.Unlock()
		return prepared.closeErr
	}
	startup := prepared.startup
	if startup == nil && prepared.bound && prepared.operation != nil {
		prepared.operation.gate.Lock()
		startup = prepared.operation.startup
		prepared.operation.gate.Unlock()
	}
	prepared.mu.Unlock()
	if startup != nil {
		// Actual pending native cancellation/whole closure joins before the saved
		// source can close. Ready already transferred to that native runtime.
		if err := startup.Close(); err != nil {
			return err
		}
	}
	prepared.mu.Lock()
	defer prepared.mu.Unlock()
	if prepared.closed {
		return prepared.closeErr
	}
	if prepared.artifacts != nil && prepared.artifacts.slot != nil {
		return prepared.closeSlotArtifactsV2()
	}
	prepared.closed = true
	var creationErr error
	if prepared.creation != nil {
		creationErr = prepared.creation.Close()
		prepared.creation = nil
	}
	if prepared.rewindCreationGroup != nil {
		creationErr = errors.Join(creationErr, prepared.rewindCreationGroup.Close())
		prepared.rewindCreationGroup = nil
	}
	prepared.closeErr = errors.Join(prepared.artifacts.Close(), prepared.vacancy.Close(), creationErr)
	return prepared.closeErr
}

// Resume consumes this actual cold source on the SAME original operation.
// The host's canonical body receives no raw absence or execution constructor.
func (prepared *ColdResumeV1) Resume(ctx context.Context, operation *SessionOperation, args api.ResumeArgs) (*api.ResumeResult, error) {
	if err := prepared.bindResumeOperationV1(ctx, operation); err != nil {
		return nil, err
	}
	return resumeColdForPreparationV1(ctx, operation, prepared, args)
}

// Only this SAME actual Cold/OP producer may consume the original binding.
// Extracting it does not permit a row, copied source or new VM lease issuer.
func (prepared *ColdResumeV1) bindResumeOperationV1(ctx context.Context, operation *SessionOperation) error {
	if err := prepared.Check(ctx, operation); err != nil {
		return err
	}
	prepared.mu.Lock()
	if prepared.closed || prepared.bound {
		prepared.mu.Unlock()
		return auxiliaryUnavailableV1("the original cold continuation was consumed or ended")
	}
	operation.gate.Lock()
	if !operation.active.Load() || operation.nativeColdResume != nil || operation.startup != nil {
		operation.gate.Unlock()
		prepared.mu.Unlock()
		return auxiliaryUnavailableV1("the original operation already owns a cold continuation or startup")
	}
	operation.nativeColdResume = prepared
	prepared.bound = true
	operation.gate.Unlock()
	prepared.mu.Unlock()
	return nil
}

// Resume sends values only after the actual same controller accepted the
// incoming original OP description. The native borrower retains it through
// worker cleanup, including parent death, explicit cancel and final release.
func (prepared *WarmResumeV1) Resume(ctx context.Context, operation *SessionOperation, args api.ResumeArgs) (*api.ResumeResult, error) {
	return prepared.resumeWithIncomingOperationV1(ctx, operation, args)
}
