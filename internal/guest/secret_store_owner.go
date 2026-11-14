package guest

import (
	"context"
	"fmt"
	"io"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/phaseclock"
	"github.com/iamlakshya/simstim/internal/proto"
)

// There is one conservative Store installation claim per guest boot. The
// current trusted data defines one Store target. Different commands/contexts
// are not guessed to be independent auth stores. No real value enters it.
type secretStoreOwnership struct{ state *secretStoreOwnerState }

type secretStoreOwnerState struct {
	gate  chan struct{}
	claim *secretStoreClaim
}

type secretStoreClaim struct {
	argv      []string
	cwd       string
	env       []string
	sentinel  api.Secret
	installed bool
}

func (secretStoreOwnership) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, api.Redacted)
}
func (*secretStoreOwnerState) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, api.Redacted)
}
func (secretStoreClaim) Format(state fmt.State, _ rune) { _, _ = io.WriteString(state, api.Redacted) }

func newSecretStoreOwnership() *secretStoreOwnership {
	return &secretStoreOwnership{state: &secretStoreOwnerState{gate: make(chan struct{}, 1)}}
}

func (claim secretStoreClaim) same(other secretStoreClaim) bool {
	return claim.sentinel == other.sentinel && claim.cwd == other.cwd && slices.Equal(claim.argv, other.argv) && slices.Equal(claim.env, other.env)
}

func (owner *secretStoreOwnership) install(ctx context.Context, name string, claim secretStoreClaim, run func() error) error {
	if ctx == nil || owner == nil || owner.state == nil || run == nil {
		return secretStoreConflict(name)
	}
	select {
	case owner.state.gate <- struct{}{}:
	case <-ctx.Done():
		return secretStoreConflict(name)
	}
	defer func() { <-owner.state.gate }()
	if ctx.Err() != nil {
		return secretStoreConflict(name)
	}
	if prior := owner.state.claim; prior != nil {
		if prior.installed && prior.same(claim) {
			return nil
		} // never rerun even an identical successful Store
		return secretStoreConflict(name)
	}
	// Pending/failed claims remain occupied. A failed Store may already have
	// changed its auth file, so retry requires a genuinely fresh guest boot.
	claim.argv = slices.Clone(claim.argv)
	claim.env = slices.Clone(claim.env)
	claim.installed = false
	owner.state.claim = &claim
	if err := run(); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return secretStoreConflict(name)
	}
	installed := claim
	installed.installed = true
	owner.state.claim = &installed
	return nil
}

// prepareMemberSecretStore is the actual common startMember call before any
// holder manifest/holder/agent. Env is still the base, before EVERY Secrets
// addition. Only explicit bound sentinels enter the selected Store command.
func (g *Init) prepareMemberSecretStore(parent context.Context, args proto.CtlStartArgs) error {
	sentinels, err := memberBoundSentinels(args)
	if err != nil || args.SecretModes == nil {
		return err
	}
	if parent == nil {
		return secretStoreError("")
	}
	ctx, cancel := phaseclock.WithDeadline(parent, phaseclock.Current.Now().Add(secretStoreTimeout))
	defer cancel()
	plans, env, err := prepareSecretStores(args.Session, args.Agent, args.Cwd, args.Env, sentinels)
	if err != nil || len(plans) == 0 {
		return err
	}
	if len(plans) != 1 {
		return secretStoreConflict(plans[0].name)
	}
	plan := plans[0]
	if g == nil {
		return secretStoreConflict(plan.name)
	}
	g.mu.Lock()
	if g.ended || !g.memberStarts[args.Session] {
		g.mu.Unlock()
		return secretStoreConflict(plan.name)
	}
	if g.secretStores == nil {
		g.secretStores = newSecretStoreOwnership()
	}
	owner := g.secretStores
	g.mu.Unlock()
	claim := g.secretStoreInvocation(plan, args.Cwd, env)
	err = owner.install(ctx, plan.name, claim, func() error {
		g.mu.Lock()
		active := !g.ended && g.memberStarts[args.Session]
		g.mu.Unlock()
		if !active {
			return secretStoreConflict(plan.name)
		}
		output := &secretStoreDiscard{remaining: maxSecretStoreOutput, cancel: cancel}
		return g.runSecretStore(ctx, args.Session, args.Cwd, env, plan, output)
	})
	if err != nil {
		return err
	}
	g.mu.Lock()
	active := !g.ended && g.memberStarts[args.Session]
	g.mu.Unlock()
	if !active || ctx.Err() != nil {
		return secretStoreConflict(plan.name)
	}
	return nil
}

// These are the SAME actual path/environment mappings used by agentCommand.
// The claim never compares dropped raw member metadata or guesses login targets.
func (g *Init) secretStoreInvocation(plan secretStorePlan, cwd string, env []string) secretStoreClaim {
	return secretStoreClaim{argv: plan.argv, cwd: g.mapValue(cwd), env: g.mapEnv(env), sentinel: plan.sentinel}
}

func secretStoreConflict(name string) error {
	message := "this guest boot cannot safely change its stored login"
	if secretStoreEnvName(name) {
		message = "the stored login for " + name + " cannot safely change in this guest boot"
	}
	return &proto.Error{Code: "secret_store", Message: message, Hint: "Stop the sandbox and start it afresh. Repeated or ambiguous Store commands cannot rewrite another live member's login."}
}
