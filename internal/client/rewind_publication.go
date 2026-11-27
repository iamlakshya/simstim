package client

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"reflect"
	"slices"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/proto"
)

// This is the actual original commit's ephemeral writer/response witness.
// Hashes and public ACK data alone cannot construct its private write proof.
type rewindPublicationWitnessV3 struct {
	mu                                   sync.Mutex
	plan                                 *RewindPreparation
	base                                 context.Context
	conn                                 *Conn
	source                               hostStateSourceWitness
	before                               *api.SandboxCohortView
	requestID                            uint64
	wire                                 [32]byte
	prepared, sent, acknowledged, stored bool
	result                               api.RewindResult
	cohort                               *api.SandboxCohortView
	receipts                             map[string]*sandboxDeliveryReceipt
}

func (p *RewindPreparation) newPublicationWitnessV3(base, publication context.Context) (*rewindPublicationWitnessV3, error) {
	if err := p.checkLocal(publication); err != nil {
		return nil, err
	}
	source, err := captureHostStateSourceWitness(publication, p.conn)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	before := p.beforeCohort.Clone()
	p.mu.Unlock()
	if before == nil || !ValidHostID(before.RunGeneration) || before.SecretState == nil || !ValidHostID(before.SecretState.Generation) {
		return nil, sandboxReceiptError()
	}
	return &rewindPublicationWitnessV3{plan: p, base: base, conn: p.conn, source: source, before: before}, nil
}

func (w *rewindPublicationWitnessV3) beforeRequestV3(ctx context.Context, c *Conn, request proto.Request) error {
	if w == nil || c != w.conn || request.Op != api.OpRewindCommitV1 || request.ID == 0 || originalRewindV1(ctx) != w.plan {
		return sandboxReceiptError()
	}
	if err := w.plan.checkLocal(ctx); err != nil {
		return err
	}
	if err := w.source.check(ctx, c); err != nil {
		return err
	}
	header, err := json.Marshal(proto.Request{ID: request.ID, Op: request.Op, Args: json.RawMessage("{}")})
	if err != nil || len(request.Args)+len(header)-2+1 > proto.MaxFrame {
		return sandboxReceiptError()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.prepared {
		return sandboxReceiptError()
	}
	w.requestID, w.wire, w.prepared = request.ID, sha256.Sum256(request.Args), true
	return nil
}

func (w *rewindPublicationWitnessV3) wroteRequestV3(c *Conn, id uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.prepared && w.conn == c && w.requestID == id {
		w.sent = true
	}
}

func cloneRewindPublicationResultV3(input api.RewindResult) api.RewindResult {
	input.Session.SecretState = api.CloneSessionSecretState(input.Session.SecretState)
	input.Session.Secrets = slices.Clone(input.Session.Secrets)
	input.Session.MissingSecrets = slices.Clone(input.Session.MissingSecrets)
	if input.Session.NamedSSHKeys != nil {
		view := *input.Session.NamedSSHKeys
		view.Installed, view.NextStart = slices.Clone(view.Installed), slices.Clone(view.NextStart)
		input.Session.NamedSSHKeys = &view
	}
	return input
}

func (w *rewindPublicationWitnessV3) afterResponseV3(ctx context.Context, c *Conn, response proto.Response, out any) error {
	w.mu.Lock()
	valid := w.prepared && w.sent && !w.acknowledged && c == w.conn && response.ID == w.requestID && response.OK && w.wire != ([32]byte{})
	w.mu.Unlock()
	result, ok := out.(*api.RewindResult)
	if !valid || !ok || result == nil || result.Validate(w.plan.selection) != nil || !ValidHostID(result.Session.RunGeneration) || result.Session.RunGeneration == w.before.RunGeneration || result.Session.SecretState == nil || !ValidHostID(result.Session.SecretState.Generation) || result.Session.SecretState.Generation == w.before.SecretState.Generation {
		return sandboxReceiptError()
	}
	expectedRoot := w.before.Snapshot.Primary
	if result.Session.Wall == nil || result.Session.Wall.Level != "microvm" || result.Session.ID == expectedRoot && result.Session.SandboxOf != "" || result.Session.ID != expectedRoot && result.Session.SandboxOf != expectedRoot {
		return sandboxReceiptError()
	}
	if err := egress.ValidateSavedPublicSessionState(result.Session.SecretState); err != nil {
		return err
	}
	if w.plan.named != nil {
		if err := w.plan.named.checkRewindAcknowledgedV1(result.Session.NamedSSHKeys); err != nil {
			return err
		}
	}
	if err := checkTrustedJudgeAcknowledgedV1(ctx, c, result.Session.SecretState); err != nil {
		return err
	}
	w.mu.Lock()
	w.result, w.acknowledged = cloneRewindPublicationResultV3(*result), true
	w.mu.Unlock()
	return nil
}

func (w *rewindPublicationWitnessV3) hasAcknowledgementV3() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.prepared && w.sent && w.acknowledged && w.requestID != 0 && w.wire != ([32]byte{})
}

func sameRewindMemberIDsV3(a, b *api.SandboxSnapshot) bool {
	return a != nil && b != nil && a.Primary == b.Primary && slices.Equal(a.Members, b.Members)
}

// The read can only restrict the actual correlated ACK. It cannot stand in
// for that ACK or mint another run, member, source, value or local name.
func (w *rewindPublicationWitnessV3) restrictAcknowledgementV3(ctx context.Context) error {
	if !w.hasAcknowledgementV3() {
		return sandboxReceiptError()
	}
	if err := w.plan.checkLocal(ctx); err != nil {
		return err
	}
	w.mu.Lock()
	ack := cloneRewindPublicationResultV3(w.result)
	w.mu.Unlock()
	view, err := w.conn.readFreshChildCohort(ctx, w.plan.selection.ID)
	if err != nil {
		return err
	}
	if !sameRewindMemberIDsV3(view.Snapshot, w.before.Snapshot) || view.RunGeneration != ack.Session.RunGeneration || view.RunGeneration == w.before.RunGeneration || view.SecretState.Generation != ack.Session.SecretState.Generation || view.SecretState.Generation == w.before.SecretState.Generation {
		return sandboxReceiptError()
	}
	w.plan.mu.Lock()
	group := cloneResumeGrantIntent(w.plan.group)
	w.plan.mu.Unlock()
	for id := range group.members {
		w.plan.mu.Lock()
		values := w.plan.values[id]
		err := egress.ValidateFreshValuesAgainstPublicState(ctx, view.SecretState, values)
		w.plan.mu.Unlock()
		if err != nil {
			return err
		}
	}
	if err := w.checkTrustedUnionV3(ctx, view.SecretState); err != nil {
		return err
	}
	if creation := group.members[w.plan.selection.ID].creation; creation != nil {
		domain := map[string]bool{}
		for _, name := range localSandboxNames(creation) {
			domain[name] = true
		}
		localACK, err := egress.ProjectLocalSessionSecretStateV1(ack.Session.SecretState, domain)
		if err != nil {
			return err
		}
		localView, err := egress.ProjectLocalSessionSecretStateV1(view.SecretState, domain)
		if err != nil || !reflect.DeepEqual(localACK, localView) {
			return sandboxReceiptError()
		}
	}
	w.mu.Lock()
	w.cohort = view.Clone()
	w.mu.Unlock()
	return w.plan.checkLocal(ctx)
}

func (w *rewindPublicationWitnessV3) checkTrustedUnionV3(ctx context.Context, state *api.SessionSecretState) error {
	judge := trustedJudgePlanOfV1(ctx)
	if judge == nil || judge.name == "" {
		if len(state.Intent.TrustedUses) != 0 {
			return trustedJudgeSourceErrorV1()
		}
		return nil
	}
	if !reflect.DeepEqual(state.Intent.TrustedUses, map[string]api.TrustedSecretUseV1{judge.name: judge.ownedUse()}) {
		return trustedJudgeSourceErrorV1()
	}
	judge.mu.Lock()
	err := egress.ValidateFreshValuesAgainstPublicState(ctx, state, judge.values)
	judge.mu.Unlock()
	return err
}

func (w *rewindPublicationWitnessV3) finishAcknowledgedV3(ctx context.Context) (api.RewindResult, error) {
	if w == nil || !w.hasAcknowledgementV3() || originalRewindV1(ctx) != w.plan {
		return api.RewindResult{}, sandboxReceiptError()
	}
	w.mu.Lock()
	stored := w.stored
	result := cloneRewindPublicationResultV3(w.result)
	w.mu.Unlock()
	if stored {
		return result, w.plan.checkLocal(ctx)
	}
	if err := w.restrictAcknowledgementV3(ctx); err != nil {
		return api.RewindResult{}, err
	}
	if err := w.prepareReceiptsV3(ctx); err != nil {
		return api.RewindResult{}, err
	}
	if err := storeRewindDeliveryReceiptsV3(ctx, w.conn, w.plan.group, w.receipts, w); err != nil {
		return api.RewindResult{}, err
	}
	w.mu.Lock()
	w.stored = true
	w.mu.Unlock()
	w.plan.mu.Lock()
	w.plan.committed = true
	w.plan.mu.Unlock()
	return result, nil
}
