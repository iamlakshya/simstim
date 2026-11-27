package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"maps"
	"reflect"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/proto"
)

func (p *armSecretsPlanV1) fetch(ctx context.Context, terminal bool) error {
	if err := p.check(ctx, true); err != nil {
		return err
	}
	p.mu.Lock()
	if p.busy || p.closed {
		p.mu.Unlock()
		return trustedJudgeSourceErrorV1()
	}
	p.busy = true
	needed, judgeNeed := slices.Clone(p.needed), p.judgeNeed
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.busy = false; p.mu.Unlock() }()
	for _, name := range needed {
		if err := p.check(ctx, true); err != nil {
			return err
		}
		p.mu.Lock()
		_, fetched := p.values[name]
		p.mu.Unlock()
		if fetched {
			continue
		}
		value, err := runSecret(ctx, name, p.commands[name], terminal && InputAllowed(ctx))
		if err != nil {
			return err
		}
		if err := p.check(ctx, true); err != nil {
			return err
		}
		p.mu.Lock()
		p.values[name] = value
		p.mu.Unlock()
	}
	if judgeNeed {
		p.mu.Lock()
		guest := maps.Clone(p.values)
		p.mu.Unlock()
		if err := p.judge.fetch(ctx, guest, terminal); err != nil {
			return err
		}
	}
	p.mu.Lock()
	p.fetched = true
	p.mu.Unlock()
	return p.check(ctx, true)
}

func (p *armSecretsPlanV1) arguments(ctx context.Context) (api.ArmSecretsArgs, error) {
	if err := p.check(ctx, true); err != nil {
		return api.ArmSecretsArgs{}, err
	}
	p.mu.Lock()
	if !p.fetched || p.busy || p.closed || p.before == nil {
		p.mu.Unlock()
		return api.ArmSecretsArgs{}, trustedJudgeSourceErrorV1()
	}
	args := api.ArmSecretsArgs{ID: p.id, Generation: p.before.SecretState.Generation, Secrets: maps.Clone(p.values)}
	judgeNeed := p.judgeNeed
	p.mu.Unlock()
	if judgeNeed {
		intent := &api.SecretDeliveryIntent{Version: api.SecretDeliveryIntentV1, TrustedUses: map[string]api.TrustedSecretUseV1{p.judge.name: p.judge.ownedUse()}}
		_, values, err := p.judge.arguments(ctx, intent, nil)
		if err != nil {
			return api.ArmSecretsArgs{}, err
		}
		args.TrustedUses, args.ProtectedTrustedSecretValues = intent.TrustedUses, values
	}
	return args, nil
}

// ArmSecrets uses the original local creation, a fresh protected cohort and
// separate guest and judge permissions before fetching any needed value.
// Attach and reconnect call it before entering the byte-exact terminal pump.
func (c *Conn) ArmSecrets(ctx context.Context, id string, terminal bool) (*api.SessionSecretState, error) {
	p, err := prepareArmSecretsV1(ctx, c, id)
	if err != nil || p == nil {
		return nil, err
	}
	p.mu.Lock()
	attempted, ack := p.attempted, api.CloneSessionSecretState(p.ack)
	written, acknowledged, requestID, wire := p.written, p.acknowledged, p.requestID, p.wire
	receiptStored := p.receiptStored
	p.mu.Unlock()
	if attempted {
		if ack == nil || !written || !acknowledged || !receiptStored || requestID == 0 || wire == ([32]byte{}) {
			return nil, armPublicationUnknownV1()
		}
		return p.recheckAcknowledged(ctx, ack)
	}
	needed, err := p.observe(ctx)
	if err != nil {
		return nil, err
	}
	if !needed {
		p.mu.Lock()
		state := api.CloneSessionSecretState(p.before.SecretState)
		p.mu.Unlock()
		return state, p.check(ctx, false)
	}
	if err := p.fetch(ctx, terminal); err != nil {
		return nil, err
	}
	p.mu.Lock()
	query, before := api.CloneTrustedSecretReadinessQueryV1(p.query), p.before.Clone()
	p.mu.Unlock()
	after, err := c.trustedJudgeArmViewV1(ctx, id, query)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(before, after) {
		return nil, trustedJudgeSourceErrorV1()
	}
	args, err := p.arguments(ctx)
	if err != nil {
		return nil, err
	}
	defer clear(args.Secrets)
	defer clear(args.ProtectedTrustedSecretValues)
	owned, release, err := c.beginGrant(ctx, "arm original session credentials", true)
	if err != nil {
		return nil, err
	}
	defer release()
	requestID, response, err := c.startCall(owned, "arm", args)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	written = p.attempted && p.requestID == requestID && requestID != 0
	p.written = written
	p.mu.Unlock()
	release()
	if !written {
		c.drop(requestID)
		return nil, armPublicationUnknownV1()
	}
	var result api.SessionSecretState
	if err := c.awaitCall(ctx, "arm", requestID, response, &result); err != nil {
		return nil, err
	}
	if err := p.checkAcknowledged(ctx, args, &result); err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.ack = api.CloneSessionSecretState(&result)
	p.acknowledged = true
	p.mu.Unlock()
	if err := p.storeAcknowledgedReceiptV1(ctx, c, &result); err != nil {
		return nil, err
	}
	return api.CloneSessionSecretState(&result), nil
}

func (p *armSecretsPlanV1) checkAcknowledged(ctx context.Context, args api.ArmSecretsArgs, result *api.SessionSecretState) error {
	if result == nil || !result.Supported() || !ValidHostID(result.Generation) || egress.ValidateSavedPublicSessionState(result) != nil {
		return trustedJudgeSourceErrorV1()
	}
	p.mu.Lock()
	before := api.CloneSessionSecretState(p.before.SecretState)
	p.mu.Unlock()
	expectedIntent := api.CloneSecretDeliveryIntent(before.Intent)
	if len(args.TrustedUses) != 0 && expectedIntent.TrustedUses == nil {
		expectedIntent.TrustedUses = map[string]api.TrustedSecretUseV1{}
	}
	for name, use := range args.TrustedUses {
		owned := use
		owned.Bind.Hosts = slices.Clone(use.Bind.Hosts)
		expectedIntent.TrustedUses[name] = owned
	}
	if !reflect.DeepEqual(expectedIntent, result.Intent) || len(before.Primary) != len(result.Primary) {
		return trustedJudgeSourceErrorV1()
	}
	for name, public := range before.Primary {
		next, exists := result.Primary[name]
		if !exists {
			return trustedJudgeSourceErrorV1()
		}
		if value, armed := args.Secrets[name]; armed {
			if !next.Armed || egress.ValidatePublicSecretValue(ctx, name, next, p.binds[name], value) != nil {
				return trustedJudgeSourceErrorV1()
			}
			public.Armed = true
		}
		if !reflect.DeepEqual(public, next) {
			return trustedJudgeSourceErrorV1()
		}
	}
	return p.check(ctx, false)
}

func beforeArmSecretsRequestV1(ctx context.Context, c *Conn, request proto.Request, input any) error {
	args, typed := input.(api.ArmSecretsArgs)
	if !typed || request.Op != "arm" {
		return trustedJudgeSourceErrorV1()
	}
	r := grantRequestOf(ctx)
	if r == nil {
		return trustedJudgeSourceErrorV1()
	}
	r.mu.Lock()
	p := r.armSecrets
	r.mu.Unlock()
	if p == nil || p.conn != c {
		return trustedJudgeSourceErrorV1()
	}
	held, owns := ctx.Value(heldKey{c.Host.Name}).(*heldLock)
	if !owns || held.gone.Load() {
		return trustedJudgeSourceErrorV1()
	}
	expected, err := p.arguments(ctx)
	if err != nil {
		return err
	}
	defer clear(expected.Secrets)
	defer clear(expected.ProtectedTrustedSecretValues)
	encoded, err := json.Marshal(args)
	if err != nil || !bytes.Equal(encoded, request.Args) || !reflect.DeepEqual(expected, args) {
		return trustedJudgeSourceErrorV1()
	}
	p.mu.Lock()
	ceiling := p.ceiling
	p.mu.Unlock()
	if err := recordSecretNames(ctx, c.Host, c.HostID(), ceiling, args.Secrets); err != nil {
		return err
	}
	for name := range args.TrustedUses {
		if err := recordRequestGrant(ctx, c.Host, c.HostID(), ceiling, Outbound{Kind: OutboundSecret, Name: name}); err != nil {
			return err
		}
	}
	if err := p.check(ctx, true); err != nil {
		return err
	}
	source, err := captureHostStateSourceWitness(ctx, c)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.attempted || p.closed || request.ID == 0 {
		return armPublicationUnknownV1()
	}
	p.source, p.requestID, p.wire = source, request.ID, sha256.Sum256(request.Args)
	p.attempted = true
	return nil
}

func armPublicationUnknownV1() error {
	return &UserError{Msg: "this credential arm request already attempted publication", Hint: "Reconnect to refresh the session and check its credential state.", Code: "secret_arm_unknown", Kind: KindRefused}
}

// A successful earlier Run belongs to this operation only. The final direct
// facade invocation still observes the actual owner; an ACK cache alone is
// neither permission to skip nor permission to publish another value frame.
func (p *armSecretsPlanV1) recheckAcknowledged(ctx context.Context, ack *api.SessionSecretState) (*api.SessionSecretState, error) {
	if err := p.check(ctx, true); err != nil {
		return nil, err
	}
	if err := withSandboxReceiptObservation(ctx, p.conn, func(owned context.Context) error { return p.source.check(owned, p.conn) }); err != nil {
		return nil, err
	}
	p.mu.Lock()
	query, before := api.CloneTrustedSecretReadinessQueryV1(p.query), p.before.Clone()
	p.mu.Unlock()
	if query != nil {
		query.Generation = ack.Generation
	}
	fresh, err := p.conn.trustedJudgeArmViewV1(ctx, p.id, query)
	if err != nil {
		return nil, err
	}
	if !sameGrantSnapshot(before.Snapshot, fresh.Snapshot) || before.RunGeneration != fresh.RunGeneration || !reflect.DeepEqual(ack, fresh.SecretState) {
		return nil, armPublicationUnknownV1()
	}
	needed, judgeNeed, err := armSecretsNeededV1(p.names, p.binds, p.judge, fresh, query)
	if err != nil {
		return nil, err
	}
	if len(needed) != 0 || judgeNeed {
		return nil, armPublicationUnknownV1()
	}
	if err := p.check(ctx, true); err != nil {
		return nil, err
	}
	return api.CloneSessionSecretState(fresh.SecretState), nil
}
