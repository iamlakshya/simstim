package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func init() { Features = append(Features, api.FeatureRewindPreparationV1) }

func (b *bridge) handleRewindPreparationV1(request proto.Request) bool {
	if request.Op != api.OpRewindPrepareV1 && request.Op != api.OpRewindPreparedCheckV1 && request.Op != api.OpRewindCommitV1 && request.Op != api.OpRewindPreparedCancelV1 {
		return false
	}
	if request.ID == 0 {
		b.reply(request.ID, nil, rewindFailureV1("rewind needs its actual accepted request"))
		return true
	}
	if err := CheckCaller(); err != nil {
		b.reply(request.ID, nil, err)
		return true
	}
	service := b.rewindServiceV1()
	if service == nil {
		b.reply(request.ID, nil, rewindFailureV1("the original client connection ended"))
		return true
	}
	if request.Op == api.OpRewindPreparedCancelV1 {
		var args api.RewindPreparedArgsV1
		if json.Unmarshal(request.Args, &args) == nil && args.Validate() == nil {
			if p := service.plan.Load(); p != nil && args.Prepared.Token == p.token && args.Selection == p.selection && p.cancel != nil {
				p.cancel(context.Canceled)
			}
		}
	}
	service.work.Lock()
	defer service.work.Unlock()
	if request.Op == api.OpRewindPrepareV1 {
		if service.closed.Load() {
			b.reply(request.ID, nil, rewindFailureV1("the original rewind service ended"))
			return true
		}
		result, err := b.prepareRewindV1(service, request)
		b.reply(request.ID, result, err)
		return true
	}
	if request.Op == api.OpRewindCommitV1 {
		var args api.RewindCommitArgsV1
		if json.Unmarshal(request.Args, &args) != nil || args.Validate() != nil {
			b.reply(request.ID, nil, rewindFailureV1("the original rewind commit is invalid"))
			return true
		}
		p := service.plan.Load()
		encoded, encodeErr := json.Marshal(args.Start)
		if encodeErr != nil {
			b.reply(request.ID, nil, encodeErr)
			return true
		}
		digest := sha256.Sum256(encoded)
		clear(encoded)
		committedDigest := hex.EncodeToString(digest[:])
		if p == nil && service.result != nil && args.Prepared.Token == service.settled && service.result.ID == args.Selection.ID && service.result.Checkpoint == args.Selection.Checkpoint && service.resultDigest == committedDigest {
			b.reply(request.ID, service.result, nil)
			return true
		}
		if p == nil || !args.Prepared.Valid() || args.Prepared.Token != p.token || args.Selection != p.selection {
			b.reply(request.ID, nil, rewindFailureV1("this connection does not own that original rewind"))
			return true
		}
		result, err := p.commitRewindV1(request, args)
		if err == nil && p.phase.Load() == rewindCompletedV1 {
			cleanup := p.closeOwnedV1()
			if cleanup == nil {
				service.settled = p.token
				service.result = result
				service.resultDigest = committedDigest
				service.plan.Store(nil)
			}
			err = errors.Join(err, cleanup)
		}
		b.reply(request.ID, result, err)
		return true
	}
	var args api.RewindPreparedArgsV1
	if json.Unmarshal(request.Args, &args) != nil || args.Validate() != nil {
		b.reply(request.ID, nil, rewindFailureV1("the original rewind continuation is invalid"))
		return true
	}
	p := service.plan.Load()
	if p == nil {
		if request.Op == api.OpRewindPreparedCancelV1 && service.settled == args.Prepared.Token {
			b.reply(request.ID, struct{}{}, nil)
		} else {
			b.reply(request.ID, nil, rewindFailureV1("this connection does not own that original rewind"))
		}
		return true
	}
	if args.Prepared.Token != p.token || args.Selection != p.selection {
		b.reply(request.ID, nil, rewindFailureV1("the rewind token selects another original operation"))
		return true
	}
	if request.Op == api.OpRewindPreparedCheckV1 {
		err := p.checkPreparationV1(p.work)
		b.reply(request.ID, p.preparedResultV1(), err)
		return true
	}
	err := p.closeOwnedV1()
	if err == nil {
		service.settled = p.token
		service.plan.Store(nil)
	}
	b.reply(request.ID, struct{}{}, err)
	return true
}

func (b *bridge) closeRewindPreparationsV1() error {
	b.mu.Lock()
	service := b.rewindPreparationService
	b.mu.Unlock()
	if service == nil {
		return nil
	}
	service.closed.Store(true)
	if p := service.plan.Load(); p != nil && p.cancel != nil {
		p.cancel(context.Canceled)
	}
	service.work.Lock()
	defer service.work.Unlock()
	p := service.plan.Load()
	if p == nil {
		return nil
	}
	var observed error
	for {
		err := p.closeOwnedV1()
		if err == nil {
			service.settled = p.token
			service.plan.Store(nil)
			return observed
		}
		// Keep every actual native actor and original OP reachable. Neither
		// transport loss nor a pending journal can settle unknown custody.
		if observed == nil {
			observed = err
			if b.log != nil {
				b.log.Warn("retaining the original unfinished rewind", "err", err)
			}
		}
		time.Sleep(time.Second)
	}
}

func (p *bridgeRewindPreparationV1) checkCommitEnvelopeV1(ctx context.Context, args api.ResumeArgs) error {
	if p == nil || ctx == nil || p.bridge == nil || p.operation == nil || !p.active.Load() || !p.consumed || p.prepareRequest == 0 || p.commitRequest == 0 || len(p.commitEncoded) == 0 || args.Prepared != nil || p.execution == nil || p.execution.Err() != nil || !p.operation.Valid(p.operation.Primary()) {
		return rewindFailureV1("the operation lost its actual original accepted rewind commit")
	}
	p.bridge.mu.Lock()
	service := p.bridge.rewindPreparationService
	p.bridge.mu.Unlock()
	if service == nil || service.plan.Load() != p {
		return rewindFailureV1("the original rewind commit is no longer registered on its accepted connection")
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return err
	}
	defer clear(encoded)
	if !equalSecretBytesV1(encoded, p.commitEncoded) {
		return rewindFailureV1("the original rewind commit values or metadata changed")
	}
	return ctx.Err()
}

func cloneRewindStartV1(args api.ResumeArgs) (api.ResumeArgs, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return api.ResumeArgs{}, err
	}
	defer clear(raw)
	var clone api.ResumeArgs
	if err := json.Unmarshal(raw, &clone); err != nil {
		return api.ResumeArgs{}, err
	}
	return clone, nil
}

func clearRewindStartV1(args *api.ResumeArgs) {
	if args == nil {
		return
	}
	clear(args.Secrets)
	clear(args.ProtectedTrustedSecretValues)
	if args.NamedSSHKeys != nil {
		clear(args.NamedSSHKeys.Values)
	}
	for i := range args.Members {
		clear(args.Members[i].Secrets)
		clear(args.Members[i].ProtectedTrustedSecretValues)
	}
}
