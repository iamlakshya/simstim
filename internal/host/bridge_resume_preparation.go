package host

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func init() { Features = append(Features, api.FeatureResumePreparationV1) }

type bridgeResumePreparationContextKeyV1 struct{}
type bridgeResumePreparationV1 struct {
	bridge       *bridge
	operation    *SessionOperation
	warm         *WarmResumeV1
	cold         *ColdResumeV1
	noneCold     *NoneColdResumeV1
	lifetime     context.Context
	cancel       context.CancelFunc
	publication  *runtimePublication
	accounting   *resumePreparationLeaseV1
	args         api.ResumeArgs
	metadata     []byte
	token        string
	active       atomic.Bool
	consumed     bool
	requestID    uint64
	finalEncoded []byte
}
type bridgeResumeServiceV1 struct {
	work    sync.Mutex
	closed  atomic.Bool
	plan    atomic.Pointer[bridgeResumePreparationV1]
	settled string
}

func (b *bridge) resumeServiceV1() *bridgeResumeServiceV1 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ctx == nil || b.ctx.Err() != nil {
		return nil
	}
	if b.resumePreparationService == nil {
		b.resumePreparationService = &bridgeResumeServiceV1{}
	}
	return b.resumePreparationService
}
func resumePreparationErrorV1(message string) error {
	return UserError("resume_preparation", message, "Prepare the resume again through the same verified client connection.")
}
func (p *bridgeResumePreparationV1) result() api.ResumePrepareResultV1 {
	mode := "cold"
	if p.warm != nil {
		mode = "warm"
	}
	return api.ResumePrepareResultV1{V: 1, Prepared: &api.PreparedResumeV1{V: 1, Token: p.token}, Mode: mode, Primary: p.operation.Primary()}
}
func (p *bridgeResumePreparationV1) check(ctx context.Context) error {
	if p == nil || !p.active.Load() || p.bridge == nil || p.lifetime == nil || p.lifetime.Err() != nil || p.bridge.ctx == nil || p.bridge.ctx.Err() != nil || p.operation == nil || !p.operation.Valid(p.operation.Primary()) {
		return resumePreparationErrorV1("the original resume preparation ended")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.warm != nil {
		if p.cold != nil || p.noneCold != nil || p.accounting != nil {
			return resumePreparationErrorV1("the native resume mode changed")
		}
		return p.warm.Check(ctx, p.operation)
	}
	if p.noneCold != nil {
		if p.cold != nil || p.accounting != nil {
			return resumePreparationErrorV1("the None continuation acquired another native source")
		}
		return p.noneCold.Check(ctx, p.operation)
	}
	if p.cold == nil || p.accounting == nil {
		return resumePreparationErrorV1("the cold preparation has no original native memory reservation")
	}
	return p.accounting.Check(ctx, p.operation, p.cold)
}
func (b *bridge) handleResumePreparationV1(req proto.Request) bool {
	if req.Op != "resume.prepare-v1" && req.Op != "resume.prepared.check-v1" && req.Op != "resume.prepared.cancel-v1" {
		return false
	}
	service := b.resumeServiceV1()
	if service == nil {
		b.reply(req.ID, nil, resumePreparationErrorV1("the original client connection ended"))
		return true
	}
	// Cancellation must interrupt the actual owner BEFORE waiting to join its
	// work turn. The immutable token selects only this connection's SAME plan.
	if req.Op == "resume.prepared.cancel-v1" {
		var token api.PreparedResumeV1
		if api.ExactJSONFields(req.Args, token) && json.Unmarshal(req.Args, &token) == nil && token.Valid() {
			if p := service.plan.Load(); p != nil && p.token == token.Token && p.cancel != nil {
				p.cancel()
			}
		}
	}
	service.work.Lock()
	defer service.work.Unlock()
	if service.closed.Load() || b.ctx == nil || b.ctx.Err() != nil {
		b.reply(req.ID, nil, resumePreparationErrorV1("the original client connection ended"))
		return true
	}
	if req.Op == "resume.prepare-v1" {
		result, err := b.prepareResumeV1(service, req.Args)
		b.reply(req.ID, result, err)
		return true
	}
	var token api.PreparedResumeV1
	if !api.ExactJSONFields(req.Args, token) || json.Unmarshal(req.Args, &token) != nil || !token.Valid() {
		b.reply(req.ID, nil, resumePreparationErrorV1("the resume continuation is invalid"))
		return true
	}
	p := service.plan.Load()
	if p == nil || token.Token != p.token {
		if req.Op == "resume.prepared.cancel-v1" && p == nil && service.settled == token.Token {
			b.reply(req.ID, struct{}{}, nil)
		} else {
			b.reply(req.ID, nil, resumePreparationErrorV1("this connection does not own that resume"))
		}
		return true
	}
	if req.Op == "resume.prepared.cancel-v1" {
		err := p.close()
		if err == nil {
			service.settled = p.token
			service.plan.Store(nil)
		}
		b.reply(req.ID, struct{}{}, err)
		return true
	}
	err := p.check(p.lifetime)
	b.reply(req.ID, p.result(), err)
	return true
}
func (b *bridge) prepareResumeV1(service *bridgeResumeServiceV1, raw json.RawMessage) (result *api.ResumePrepareResultV1, failure error) {
	var args api.ResumePrepareArgsV1
	fields, err := api.JSONObjectFields(raw, "v", "start")
	if err != nil || len(fields) != 2 || !api.ExactJSONFields(raw, args) || json.Unmarshal(raw, &args) != nil || args.V != 1 || !api.ValidSessionID(args.Start.ID) {
		return nil, resumePreparationErrorV1("the original resume metadata is invalid")
	}
	startFields, err := api.JSONObjectFields(fields["start"])
	if err != nil {
		return nil, err
	}
	for _, field := range []string{"prepared", "named_ssh_keys", "secrets", "members", "secret_delivery", "protected_trusted_secret_values", "protected_trusted_secret_values_v1"} {
		if _, present := startFields[field]; present {
			return nil, resumePreparationErrorV1("resume preparation cannot carry private values or delivery requests")
		}
	}
	metadata, err := api.ResumePreparationMetadataV1(args.Start)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	if prior := service.plan.Load(); prior != nil {
		if !bytes.Equal(encoded, prior.metadata) || prior.consumed {
			return nil, resumePreparationErrorV1("this connection already owns another original resume")
		}
		if err := prior.check(prior.lifetime); err != nil {
			return nil, err
		}
		result := prior.result()
		return &result, nil
	}
	p := &bridgeResumePreparationV1{bridge: b, args: metadata, metadata: encoded}
	ctx, cancel := context.WithCancel(b.ctx)
	p.cancel = cancel
	p.lifetime = context.WithValue(ctx, bridgeResumePreparationContextKeyV1{}, p)
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	p.token = hex.EncodeToString(nonce[:])
	complete := false
	defer func() {
		if !complete {
			cleanup := p.close()
			failure = errors.Join(failure, cleanup)
			if cleanup == nil && service.plan.Load() == p {
				service.plan.Store(nil)
			}
		}
	}()
	service.plan.Store(p)
	p.operation, err = BeginSessionOperation(p.lifetime, args.Start.ID, 5*time.Second)
	if err != nil {
		return nil, err
	}
	p.active.Store(true)
	if err := checkResumeExpectedImageV1(p.lifetime, p.operation, args.Start.ExpectedImageV1); err != nil {
		return nil, err
	}
	current, loadErr := p.operation.LoadContext(p.lifetime)
	if loadErr != nil {
		return nil, loadErr
	}
	if IsNoneSupervisor(current) && (current.Run.BootID != BootID() || validNoneCleanupV1(current, BootID())) {
		p.noneCold, err = captureNoneColdResumeV1(p.lifetime, p)
	} else {
		p.warm, p.cold, err = InspectResumeForOperationV1(p.lifetime, p.operation)
	}
	if err != nil {
		return nil, err
	}
	if p.noneCold != nil {
		p.publication, err = beginRuntimePublicationKind(p.lifetime, "resume_prepare", nil, p.operation)
		if err != nil {
			return nil, err
		}
		if err := admitRestartMemoryRequestV1(p.lifetime, p.operation, p.args); err != nil {
			return nil, err
		}
	}
	if p.cold != nil {
		p.publication, err = beginRuntimePublicationKind(p.lifetime, "resume_prepare", nil, p.operation)
		if err != nil {
			return nil, err
		}
		if err := admitRestartMemoryRequestV1(p.lifetime, p.operation, p.args); err != nil {
			return nil, err
		}
		preview, err := p.operation.LoadContext(p.lifetime)
		if err != nil {
			return nil, err
		}
		if err := ApplyResumeLayers(preview, p.args.Layers); err != nil {
			return nil, err
		}
		if err := ApplyRepositoryResume(preview, p.args); err != nil {
			return nil, err
		}
		if err := applyAdmittedRestartMemoryV1(p.operation, preview); err != nil {
			return nil, err
		}
		if err := p.cold.CheckManifest(p.lifetime, p.operation, preview); err != nil {
			return nil, err
		}
		p.accounting, err = beginResumePreparationV1(p.lifetime, p.operation, p.cold, preview, p.token)
		if err != nil {
			return nil, err
		}
	}
	if err := p.check(p.lifetime); err != nil {
		return nil, err
	}
	service.plan.Store(p)
	complete = true
	view := p.result()
	return &view, nil
}

// resumePreparedV1 is called only by this same bridge's actual decoded resume.
// The opaque token never recreates an OP or switches a failed warm plan cold.
func (b *bridge) resumePreparedV1(request proto.Request, a api.ResumeArgs) (*api.ResumeResult, error) {
	service := b.resumeServiceV1()
	if service == nil {
		return nil, resumePreparationErrorV1("the original client connection ended")
	}
	service.work.Lock()
	defer service.work.Unlock()
	p := service.plan.Load()
	if service.closed.Load() || p == nil || !a.Prepared.Valid() || a.Prepared.Token != p.token || p.consumed {
		return nil, resumePreparationErrorV1("the original resume continuation is absent or consumed")
	}
	metadata, err := api.ResumePreparationMetadataV1(a)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(metadata)
	if err != nil || !bytes.Equal(encoded, p.metadata) {
		return nil, resumePreparationErrorV1("the final resume changed its original metadata")
	}
	if err := p.check(p.lifetime); err != nil {
		return nil, err
	}
	if request.Op != "resume" || request.ID == 0 {
		return nil, resumePreparationErrorV1("the cold continuation has no actual original request")
	}
	finalEncoded, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	p.requestID, p.finalEncoded = request.ID, finalEncoded
	defer func() {
		clear(a.ProtectedTrustedSecretValues)
		clearProtectedTrustedMembersV1(a.Members)
	}()
	p.consumed = true
	var result *api.ResumeResult
	if p.warm != nil {
		if a.NamedSSHKeys != nil {
			return nil, resumePreparationErrorV1("a warm resume cannot receive named keys")
		}
		result, err = p.warm.Resume(p.lifetime, p.operation, a)
	} else if p.noneCold != nil {
		result, err = p.noneCold.Resume(p.lifetime, p.operation, a)
	} else {
		result, err = p.cold.Resume(p.lifetime, p.operation, a)
	}
	cleanup := p.close()
	if cleanup == nil {
		service.settled = p.token
		service.plan.Store(nil)
	}
	return result, errors.Join(err, cleanup)
}

func (p *bridgeResumePreparationV1) close() error {
	if p == nil {
		return nil
	}
	// The service work turn joins actual operations before source closure.
	// Cancellation narrows lifetime; only the native owner can settle cleanup.
	if p.cancel != nil {
		p.cancel()
	}
	if p.accounting != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		ctx = context.WithValue(ctx, bridgeResumePreparationContextKeyV1{}, p)
		cleanup := p.accounting.Cancel(ctx, p.operation, p.cold)
		cancel()
		if cleanup != nil {
			return cleanup
		}
	}
	if p.warm != nil {
		if err := p.warm.Close(); err != nil {
			return err
		}
	}
	if p.cold != nil {
		if err := p.cold.Close(); err != nil {
			return err
		}
	}
	if p.noneCold != nil {
		if err := p.noneCold.Close(); err != nil {
			return err
		}
	}
	if p.publication != nil {
		if err := p.publication.close(); err != nil {
			return err
		}
	}
	// An unconfirmed native release above retains this SAME active OP/source.
	p.active.Store(false)
	if p.operation != nil {
		if err := p.operation.CloseWithError(); err != nil {
			return err
		}
	}
	clear(p.finalEncoded)
	p.finalEncoded = nil
	p.requestID = 0
	return nil
}
func (b *bridge) closeResumePreparationsV1() error {
	b.mu.Lock()
	service := b.resumePreparationService
	b.mu.Unlock()
	if service == nil {
		return nil
	}
	service.closed.Store(true)
	if p := service.plan.Load(); p != nil && p.cancel != nil {
		p.cancel()
	}
	service.work.Lock()
	defer service.work.Unlock()
	err := service.plan.Load().close()
	if err == nil {
		service.plan.Store(nil)
		b.mu.Lock()
		if b.resumePreparationService == service {
			b.resumePreparationService = nil
		}
		b.mu.Unlock()
	}
	return err
}
