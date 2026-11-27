package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func (p *trustedJudgePlanV1) questionScope(tier Tier, kind OutboundKind) grantScope {
	return grantScope{host: p.conn.Host.Name, hostID: p.conn.HostID(), tier: tier, kind: kind, name: p.scope, purpose: grantRemote, operation: p.family}
}

func (p *trustedJudgePlanV1) allowSecret(ctx context.Context, tier Tier, settings Settings) error {
	outbound := Outbound{Kind: OutboundSecret, Name: p.name}
	if err := requirePurposeOutbound(ctx, p.request, outbound); err != nil {
		return err
	}
	if err := p.checkMachineExposureV1(ctx, settings); err != nil {
		return err
	}
	if IsSessionMachineV1(p.conn.Host) {
		tier = min(tier, TierShared)
	}
	scope := p.questionScope(tier, outboundTrustedJudgeV1)
	p.request.mu.Lock()
	outbound.Confirmed = p.request.confirmed[scope]
	p.request.mu.Unlock()
	err := Allow(p.conn.Host, tier, outbound, settings)
	var question *ConfirmError
	if errors.As(err, &question) {
		copy := *question
		copy.Prompt = "Send " + p.name + " for the configured trusted classifier on " + p.conn.Host.Name + "? Its owner can read it."
		return &scopedGrantConfirmation{ConfirmError: &copy, scope: scope, request: p.request}
	}
	return err
}

func (p *trustedJudgePlanV1) allowProcessRisk(ctx context.Context, tier Tier) error {
	scope := p.questionScope(tier, OutboundTrustedJudgeOwnV1)
	p.request.mu.Lock()
	confirmed := p.request.confirmed[scope]
	p.request.mu.Unlock()
	if confirmed {
		return nil
	}
	return &scopedGrantConfirmation{scope: scope, request: p.request, ConfirmError: &ConfirmError{Kind: ConfirmYesNo, Host: p.conn.Host.Name, Grant: Outbound{Kind: OutboundTrustedJudgeOwnV1, Name: p.name}, Prompt: "Allow the own-tier classifier credential " + p.name + " on this process wall? A compromised process may attack its trusted helper.", Err: &UserError{Msg: "an own-tier classifier credential on a process wall needs its separate terminal answer", Hint: "Choose a stronger wall or a suitably tiered classifier credential, or confirm this exact request in an interactive terminal.", Code: "judge_key_process_risk", Kind: KindRefused}}}
}

func trustedJudgePlanOfV1(ctx context.Context) *trustedJudgePlanV1 {
	r := grantRequestOf(ctx)
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.trustedJudge
}

func (p *trustedJudgePlanV1) fetch(ctx context.Context, guestValues map[string]api.Secret, terminal bool) error {
	if err := p.check(ctx); err != nil {
		return err
	}
	p.mu.Lock()
	if p.busy || p.closed {
		p.mu.Unlock()
		return trustedJudgeSourceErrorV1()
	}
	if p.fetched {
		if guestValue, shared := guestValues[p.name]; shared && guestValue != p.values[p.name] {
			p.mu.Unlock()
			return trustedJudgeSourceErrorV1()
		}
		p.mu.Unlock()
		return nil
	}
	p.busy = true
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.busy = false; p.mu.Unlock() }()
	values := map[string]api.Secret{}
	if p.name != "" {
		value, shared := guestValues[p.name]
		if shared {
			// The actual selected guest set must independently contain this
			// name. A fabricated map cannot become a second value producer.
			p.mu.Lock()
			newPlan := p.newPlan
			p.mu.Unlock()
			allowed := newPlan != nil && slices.Contains(newPlan.selection.names, p.name)
			if p.creation != nil {
				allowed = slices.Contains(localSandboxNames(p.creation), p.name)
			}
			if !allowed || value == "" {
				return trustedJudgeSourceErrorV1()
			}
		} else {
			var err error
			value, err = runSecret(ctx, p.name, p.command, terminal && InputAllowed(ctx))
			if err != nil {
				return err
			}
		}
		values[p.name] = value
	}
	if err := p.check(ctx); err != nil {
		return err
	}
	p.mu.Lock()
	p.values = values
	p.fetched = true
	p.mu.Unlock()
	return nil
}

func fetchTrustedJudgeNewV1(ctx context.Context, p *newSecretPlan, guestValues map[string]api.Secret, terminal bool) error {
	plan := trustedJudgePlanOfV1(ctx)
	if plan == nil {
		return nil
	}
	plan.mu.Lock()
	samePlan := plan.newPlan == p
	plan.mu.Unlock()
	if !samePlan || plan.family != grantOpNew {
		return trustedJudgeSourceErrorV1()
	}
	return plan.fetch(ctx, guestValues, terminal)
}

func (p *trustedJudgePlanV1) arguments(ctx context.Context, intent *api.SecretDeliveryIntent, values map[string]api.Secret) (*api.SecretDeliveryIntent, map[string]api.Secret, error) {
	if err := p.check(ctx); err != nil {
		return nil, nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.busy || p.closed {
		return nil, nil, trustedJudgeSourceErrorV1()
	}
	if p.name == "" {
		if len(values) != 0 || intent != nil && len(intent.TrustedUses) != 0 {
			return nil, nil, trustedJudgeSourceErrorV1()
		}
		return intent, nil, nil
	}
	if intent == nil {
		return nil, nil, trustedJudgeSourceErrorV1()
	}
	expected := map[string]api.TrustedSecretUseV1{p.name: p.ownedUse()}
	if !reflect.DeepEqual(intent.TrustedUses, expected) {
		return nil, nil, trustedJudgeSourceErrorV1()
	}
	if len(values) != 0 && !sameSelectedSecretValues(values, p.values) {
		return nil, nil, trustedJudgeSourceErrorV1()
	}
	if !p.fetched {
		if len(values) != 0 {
			return nil, nil, trustedJudgeSourceErrorV1()
		}
		return intent, nil, nil
	}
	owned := api.CloneSecretDeliveryIntent(*intent)
	return &owned, maps.Clone(p.values), nil
}

func applyTrustedJudgeNewV1(ctx context.Context, p *newSecretPlan, a NewArgs) (NewArgs, error) {
	plan := trustedJudgePlanOfV1(ctx)
	if plan == nil {
		if len(a.ProtectedTrustedSecretValues) != 0 || a.SecretDelivery != nil && len(a.SecretDelivery.TrustedUses) != 0 {
			return a, trustedJudgeSourceErrorV1()
		}
		return a, nil
	}
	plan.mu.Lock()
	samePlan := plan.newPlan == p
	plan.mu.Unlock()
	if !samePlan || plan.family != grantOpNew {
		return a, trustedJudgeSourceErrorV1()
	}
	intent, values, err := plan.arguments(ctx, a.SecretDelivery, a.ProtectedTrustedSecretValues)
	if err != nil {
		return a, err
	}
	a.SecretDelivery, a.ProtectedTrustedSecretValues = intent, values
	return a, nil
}

func beforeTrustedJudgeRequestV1(ctx context.Context, c *Conn, request proto.Request, args any) error {
	if request.Op == "arm" {
		return beforeArmSecretsRequestV1(ctx, c, request, args)
	}
	if request.Op == api.OpRewindCommitV1 {
		commit, ok := args.(api.RewindCommitArgsV1)
		if !ok || originalRewindV1(ctx) == nil {
			return trustedJudgeSourceErrorV1()
		}
		raw, err := json.Marshal(commit.Start)
		if err != nil {
			return err
		}
		if err := checkTrustedJudgeWireFieldsV1(raw, true); err != nil {
			return err
		}
	}
	switch request.Op {
	case "new", "resume", "spawn", "arm":
		allowRoot := false
		switch args.(type) {
		case NewArgs, ResumeArgs, api.ArmSecretsArgs:
			allowRoot = true
		}
		if err := checkTrustedJudgeWireFieldsV1(request.Args, allowRoot); err != nil {
			return err
		}
	}
	var intent *api.SecretDeliveryIntent
	var values map[string]api.Secret
	switch a := args.(type) {
	case NewArgs:
		if request.Op != "new" {
			return trustedJudgeSourceErrorV1()
		}
		intent, values = a.SecretDelivery, a.ProtectedTrustedSecretValues
	case ResumeArgs:
		if request.Op != "resume" {
			return trustedJudgeSourceErrorV1()
		}
		intent, values = a.SecretDelivery, a.ProtectedTrustedSecretValues
	case api.RewindCommitArgsV1:
		if request.Op != api.OpRewindCommitV1 || originalRewindV1(ctx) == nil {
			return trustedJudgeSourceErrorV1()
		}
		intent, values = a.Start.SecretDelivery, a.Start.ProtectedTrustedSecretValues
	default:
		return nil
	}
	plan := trustedJudgePlanOfV1(ctx)
	if plan == nil {
		if len(values) != 0 || intent != nil && len(intent.TrustedUses) != 0 {
			return trustedJudgeSourceErrorV1()
		}
		return nil
	}
	if plan.conn != c || request.Op == "new" && plan.family != grantOpNew || (request.Op == "resume" || request.Op == api.OpRewindCommitV1) && plan.family != grantOpResume {
		return trustedJudgeSourceErrorV1()
	}
	encoded, err := json.Marshal(args)
	if err != nil || !bytes.Equal(encoded, request.Args) {
		return trustedJudgeSourceErrorV1()
	}
	held, owns := ctx.Value(heldKey{c.Host.Name}).(*heldLock)
	if !owns || held.gone.Load() {
		return trustedJudgeSourceErrorV1()
	}
	if err := plan.check(ctx); err != nil {
		return err
	}
	plan.mu.Lock()
	fetched := plan.fetched && !plan.busy && !plan.closed && sameSelectedSecretValues(values, plan.values)
	cold, ceiling := plan.cold, plan.ceiling
	plan.mu.Unlock()
	if plan.family == grantOpResume {
		mode, err := retainedNativeResumeModeLocalV1(ctx, c, plan.id)
		if err != nil {
			return err
		}
		if mode == "warm" {
			if len(values) != 0 || intent != nil && len(intent.TrustedUses) != 0 {
				return trustedJudgeSourceErrorV1()
			}
			return nil
		}
		if mode != "cold" && mode != "rewind" || !cold {
			return trustedJudgeSourceErrorV1()
		}
	}

	if plan.name != "" && (!fetched || intent == nil || !reflect.DeepEqual(intent.TrustedUses, map[string]api.TrustedSecretUseV1{plan.name: plan.ownedUse()})) {
		return trustedJudgeSourceErrorV1()
	}
	if plan.name != "" {
		tier, _, err := grantHostTier(ctx, c.Host)
		if err != nil {
			return err
		}
		tier = min(tier, ceiling, requestCeiling(ctx, tier))
		if err := recordRequestGrant(ctx, c.Host, c.HostID(), tier, Outbound{Kind: OutboundSecret, Name: plan.name}); err != nil {
			return err
		}
	}
	return plan.check(ctx)
}

func (p *trustedJudgePlanV1) close() error {
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

// The original New fetcher calls this immediately before any guest value
// command. Preparation questions cannot waive a later tier or source change.
func checkTrustedJudgeNewBeforeValuesV1(ctx context.Context, p *newSecretPlan) error {
	plan := trustedJudgePlanOfV1(ctx)
	if plan == nil {
		return nil
	}
	plan.mu.Lock()
	samePlan := plan.newPlan == p
	plan.mu.Unlock()
	if !samePlan || plan.family != grantOpNew {
		return trustedJudgeSourceErrorV1()
	}
	return plan.check(ctx)
}
