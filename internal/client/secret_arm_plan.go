package client

import (
	"context"
	"fmt"
	"io"
	"reflect"
	"slices"
	"sort"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
)

// armSecretsPlanV1 retains one original arm operation. It selects no name
// from host display rows and gives no caller access to its private values.
type armSecretsPlanV1 struct {
	mu                              sync.Mutex
	receipt                         *sandboxDeliveryReceipt
	receiptAttempted, receiptStored bool
	source                          hostStateSourceWitness
	requestID                       uint64
	wire                            [32]byte
	written, acknowledged           bool
	request                         *GrantRequest
	conn                            *Conn
	stamp                           *hostTransportStamp
	operation                       *settingsOperation
	creation                        *SandboxPolicy
	judge                           *trustedJudgePlanV1
	id                              string
	ceiling                         Tier
	names                           []string
	binds                           map[string]api.SecretBind
	commands                        map[string]string
	before                          *api.SandboxCohortView
	query                           *api.TrustedSecretReadinessQueryV1
	needed                          []string
	judgeNeed                       bool
	values                          map[string]api.Secret
	busy                            bool
	fetched                         bool
	closed                          bool
	attempted                       bool
	ack                             *api.SessionSecretState
}

func (*armSecretsPlanV1) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }

func (p *armSecretsPlanV1) close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.busy {
		return agentModeResourceError()
	}
	clear(p.values)
	p.values = nil
	p.closed = true
	return nil
}

func prepareArmSecretsV1(ctx context.Context, c *Conn, id string) (*armSecretsPlanV1, error) {
	r := grantRequestOf(ctx)
	if r == nil || c == nil || !api.ValidSessionID(id) {
		return nil, trustedJudgeSourceErrorV1()
	}
	r.mu.Lock()
	original := r.id == id && r.operation == grantOpSecretArm
	before := r.armSecrets
	r.mu.Unlock()
	if !original {
		return nil, trustedJudgeSourceErrorV1()
	}
	if before != nil {
		if before.conn != c || before.id != id {
			return nil, trustedJudgeSourceErrorV1()
		}
		return before, before.check(ctx, false)
	}
	if err := requireGrantOperation(ctx, grantOpSecretArm); err != nil {
		return nil, err
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return nil, err
	}
	tier, _, err := EffectiveSessionTier(ctx, c.Host, id, c.HostID())
	if err != nil {
		return nil, err
	}
	creation, err := getSandboxReceiptPolicyContext(ctx, c, id)
	if err != nil {
		return nil, err
	}
	if creation == nil || creation.CreationWall == "none" {
		return nil, nil
	}
	if !tier.Valid() || !ValidHostID(c.HostID()) || creation.HostID != c.HostID() || creation.FutureDelivery || creation.FutureLineage || creation.futureDeliveryReceipt || creation.futureInputOperation || creation.inputOperation != nil {
		return nil, trustedJudgeSourceErrorV1()
	}
	operation, err := r.borrowNewSettings(ctx)
	if err != nil {
		return nil, err
	}
	settings, err := operation.Settings(ctx)
	if err != nil {
		return nil, err
	}
	view, err := NormaliseSettingsSecrets(settings)
	if err != nil {
		return nil, err
	}
	p := &armSecretsPlanV1{request: r, conn: c, stamp: c.hostTransport, operation: operation, creation: cloneSandboxPolicy(creation), id: id, ceiling: tier, binds: map[string]api.SecretBind{}, commands: map[string]string{}, values: map[string]api.Secret{}}
	if intent := creation.DeliveryIntent; intent != nil {
		if intent.Version != api.SecretDeliveryIntentV1 {
			return nil, trustedJudgeSourceErrorV1()
		}
		allowed := localSandboxNames(creation)
		bindings := api.CloneSecretBinds(intent.Binds)
		for name, mode := range intent.Modes {
			if mode != api.SecretModeBound {
				continue
			}
			if !slices.Contains(allowed, name) || view.Kinds[name] != "" || view.Commands[name] == "" || !reflect.DeepEqual(view.Binds[name], intent.Binds[name]) {
				return nil, trustedJudgeSourceErrorV1()
			}
			if receipt := creation.deliveryReceipt; receipt != nil {
				if !slices.Contains(receipt.Allowed, name) || receipt.State == nil || receipt.State.Intent.Modes[name] != api.SecretModeBound || !reflect.DeepEqual(receipt.State.Intent.Binds[name], intent.Binds[name]) {
					return nil, sandboxReceiptError()
				}
			}
			p.names = append(p.names, name)
			p.commands[name], p.binds[name] = view.Commands[name], bindings[name]
		}
	}
	sort.Strings(p.names)
	layers, err := resumeImageLayersV1(ctx, operation, c.Host, creation)
	if err != nil {
		return nil, err
	}
	p.judge, err = captureTrustedJudgeV1(ctx, c, operation, layers, creation.CreationWall, grantOpSecretArm, id, creation, nil)
	if err != nil {
		return nil, err
	}
	if len(p.names) == 0 && (p.judge == nil || p.judge.name == "") {
		return nil, nil
	}
	r.mu.Lock()
	if err := r.lifetimeErrorLocked(); err != nil {
		r.mu.Unlock()
		return nil, err
	}
	if r.armSecrets != nil {
		r.mu.Unlock()
		return nil, trustedJudgeSourceErrorV1()
	}
	r.armSecrets = p
	r.mu.Unlock()
	return p, p.check(ctx, false)
}

func (p *armSecretsPlanV1) check(ctx context.Context, grants bool) error {
	if p == nil || ctx == nil || ctx.Err() != nil || grantRequestOf(ctx) != p.request || p.conn.hostTransport != p.stamp {
		return trustedJudgeSourceErrorV1()
	}
	p.request.mu.Lock()
	original := p.request.armSecrets == p
	lifetime := p.request.lifetimeErrorLocked()
	p.request.mu.Unlock()
	if !original || lifetime != nil {
		return trustedJudgeSourceErrorV1()
	}
	if err := requireGrantOperation(ctx, grantOpSecretArm); err != nil {
		return err
	}
	if err := bindRequestTransportSource(ctx, p.conn); err != nil {
		return err
	}
	p.mu.Lock()
	closed, ceiling := p.closed, p.ceiling
	needed := slices.Clone(p.needed)
	p.mu.Unlock()
	if closed {
		return trustedJudgeSourceErrorV1()
	}
	hostTier, hid, err := grantHostTier(ctx, p.conn.Host)
	if err != nil {
		return err
	}
	if hid != p.conn.HostID() {
		return grantIdentityError(p.conn.Host)
	}
	tier, _, err := EffectiveSessionTier(ctx, p.conn.Host, p.id, p.conn.HostID())
	if err != nil {
		return err
	}
	tier = min(tier, hostTier)
	creation, err := getSandboxReceiptPolicyContext(ctx, p.conn, p.id)
	if err != nil {
		return err
	}
	p.mu.Lock()
	expectedReceipt := p.creation.deliveryReceipt
	if p.receiptAttempted && p.receiptStored {
		expectedReceipt = p.receipt
	}
	p.mu.Unlock()
	if !sameSandboxCreation(creation, p.creation) || !reflect.DeepEqual(creation.deliveryReceipt, expectedReceipt) || creation.futureDeliveryReceipt || creation.futureInputOperation || creation.inputOperation != nil {
		return trustedJudgeSourceErrorV1()
	}
	tier = min(tier, ceiling)
	p.mu.Lock()
	p.ceiling = min(p.ceiling, tier)
	p.mu.Unlock()
	if p.judge != nil {
		p.judge.mu.Lock()
		p.judge.ceiling = min(p.judge.ceiling, tier)
		p.judge.mu.Unlock()
	}
	settings, err := p.operation.Settings(ctx)
	if err != nil {
		return err
	}
	view, err := NormaliseSettingsSecrets(settings)
	if err != nil {
		return err
	}
	for _, name := range p.names {
		if view.Kinds[name] != "" || view.Commands[name] != p.commands[name] || !reflect.DeepEqual(view.Binds[name], p.binds[name]) {
			return trustedJudgeSourceErrorV1()
		}
	}
	if grants {
		if _, err := ChooseWall(p.conn.Host, tier, p.creation.CreationWall, false); err != nil {
			return err
		}
		wall := &wallGrantIntent{level: p.creation.CreationWall, exposed: map[string]bool{}}
		for _, name := range p.creation.Exposed {
			wall.exposed[name] = true
		}
		if err := allowSecretNamesAtWall(ctx, p.conn.Host, p.conn.HostID(), tier, settings, needed, wall, true); err != nil {
			return err
		}
		if p.judge != nil {
			if err := p.judge.check(ctx); err != nil {
				return err
			}
		}
	}
	return p.operation.Revalidate(ctx)
}

func (p *armSecretsPlanV1) observe(ctx context.Context) (bool, error) {
	view, err := p.conn.trustedJudgeArmViewV1(ctx, p.id, nil)
	if err != nil {
		return false, err
	}
	var query *api.TrustedSecretReadinessQueryV1
	if p.judge != nil && p.judge.name != "" {
		live, err := p.conn.Policy(ctx, p.id)
		if err != nil {
			return false, err
		}
		if !reflect.DeepEqual(live.Classifier, p.judge.classifier) {
			return false, trustedJudgeSourceErrorV1()
		}
		query = &api.TrustedSecretReadinessQueryV1{Version: 1, Generation: view.SecretState.Generation, Uses: map[string]api.TrustedSecretUseV1{p.judge.name: p.judge.ownedUse()}}
		fresh, err := p.conn.trustedJudgeArmViewV1(ctx, p.id, query)
		if err != nil {
			return false, err
		}
		if !sameGrantSnapshot(view.Snapshot, fresh.Snapshot) || view.RunGeneration != fresh.RunGeneration || !reflect.DeepEqual(view.SecretState, fresh.SecretState) {
			return false, trustedJudgeSourceErrorV1()
		}
		view = fresh
	}
	needed, judgeNeed, err := armSecretsNeededV1(p.names, p.binds, p.judge, view, query)
	if err != nil {
		return false, err
	}
	p.mu.Lock()
	if p.before != nil && !reflect.DeepEqual(p.before, view) {
		p.mu.Unlock()
		return false, trustedJudgeSourceErrorV1()
	}
	p.before, p.query, p.needed, p.judgeNeed = view.Clone(), api.CloneTrustedSecretReadinessQueryV1(query), needed, judgeNeed
	p.mu.Unlock()
	if p.judge != nil {
		p.judge.mu.Lock()
		p.judge.armNeeded, p.judge.generation = judgeNeed, view.SecretState.Generation
		p.judge.mu.Unlock()
	}
	return len(needed) != 0 || judgeNeed, nil
}

func armSecretsNeededV1(names []string, binds map[string]api.SecretBind, judge *trustedJudgePlanV1, view *api.SandboxCohortView, query *api.TrustedSecretReadinessQueryV1) ([]string, bool, error) {
	if view == nil || view.SecretState == nil || !view.SecretState.Supported() {
		return nil, false, trustedJudgeSourceErrorV1()
	}
	var needed []string
	for _, name := range names {
		state, exists := view.SecretState.Primary[name]
		if !exists || state.Mode != api.SecretModeBound || view.SecretState.Intent.Modes[name] != api.SecretModeBound || !reflect.DeepEqual(binds[name], view.SecretState.Intent.Binds[name]) {
			return nil, false, trustedJudgeSourceErrorV1()
		}
		if !state.Armed {
			needed = append(needed, name)
		}
	}
	judgeNeed := false
	if judge != nil && judge.name != "" {
		if query == nil || view.TrustedReadiness == nil || !view.TrustedReadiness.ValidFor(*query) {
			return nil, false, trustedJudgeSourceErrorV1()
		}
		judgeNeed = slices.Contains(view.TrustedReadiness.Unarmed, judge.name)
		if slices.Contains(names, judge.name) && (judgeNeed || slices.Contains(needed, judge.name)) {
			judgeNeed = true
			if !slices.Contains(needed, judge.name) {
				needed = append(needed, judge.name)
			}
		}
	}
	sort.Strings(needed)
	return needed, judgeNeed, nil
}
