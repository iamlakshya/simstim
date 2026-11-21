//go:build linux

package host

import (
	"context"
	"errors"

	"github.com/iamlakshya/simstim/internal/proto"
)

func (warm *WarmResumeV1) stopForRewindV1(ctx context.Context, p *bridgeRewindPreparationV1) (*StoppedSessionV1, error) {
	if p == nil || p.warm != warm || p.phase.Load() != rewindStoppingV1 {
		return nil, rewindFailureV1("the stop has no actual original rewind commit")
	}
	if err := p.checkCommitEnvelopeV1(ctx, p.fresh); err != nil {
		return nil, err
	}
	if err := warm.Check(ctx, p.operation); err != nil {
		return nil, err
	}
	owner := warm.owner
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if warm.used || owner.operation != p.operation || owner.stopSent || owner.settled || owner.closed || !warm.loanSent || owner.invocation == nil || owner.holder == nil {
		return nil, rewindFailureV1("the same original live rewind stop was already consumed or changed")
	}
	if err := errors.Join(owner.invocation.Check(ctx), owner.holder.check(ctx), p.operation.native.rootRef.CheckRoot(owner.holder.root), ctx.Err()); err != nil {
		return nil, err
	}
	args := proto.NativeStopArgsV1{Version: 1, Primary: p.operation.Primary(), Nonce: owner.state.Nonce}
	owner.stopSent = true
	warm.used = true
	var accepted proto.NativeStopStateV1
	err := owner.nativeStopRequestV1(ctx, proto.NativeRewindStopRequestV1, proto.NativeRewindStopV1, args, &accepted)
	if err == nil && accepted != owner.state {
		err = rewindFailureV1("the original native rewind stop accepted another scope")
	}
	if settled := owner.settleNativeStopV1(); settled != nil {
		return nil, errors.Join(err, settled)
	}
	// Physical exit closes the actual source borrow. A lost transport ACK
	// remains an error and never creates stopped ownership on its own.
	warm.loanSent = false
	if check := owner.checkStoppedNativeFactsV1(context.Background()); check != nil {
		return nil, errors.Join(err, check)
	}
	return owner, err
}

func (warm *WarmResumeV1) settleOriginalRewindStopV1(ctx context.Context, p *bridgeRewindPreparationV1) (*StoppedSessionV1, error) {
	if warm == nil || warm.owner == nil || p == nil || p.warm != warm || p.operation == nil || p.phase.Load() != rewindStoppingV1 {
		return nil, rewindFailureV1("there is no actual original rewind stop attempt to settle")
	}
	if err := p.checkCommitEnvelopeV1(ctx, p.fresh); err != nil {
		return nil, err
	}
	owner := warm.owner
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.operation != p.operation || owner.closed || !owner.stopSent || !warm.used || owner.invocation == nil || owner.manager == nil || owner.origin == nil || !p.operation.Valid(p.operation.Primary()) {
		return nil, rewindFailureV1("the original native rewind stop attempt changed before settlement")
	}
	if err := owner.settleNativeStopV1(); err != nil {
		return nil, err
	}
	if err := owner.checkStoppedNativeFactsV1(ctx); err != nil {
		return nil, err
	}
	warm.loanSent = false
	return owner, nil
}
