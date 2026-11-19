package client

import (
	"context"
	"maps"

	"github.com/iamlakshya/simstim/internal/api"
)

// StartFreshChild performs one actual fresh local selection/fetch/spawn. Its
// host snapshot and current public cohort restrict the immutable local plan;
// neither can select another value or mint its eventual local ACK receipt.
func (c *Conn) StartFreshChild(ctx context.Context, choice api.JoinArgs, terminal bool) (*Session, error) {
	args, err := c.PrepareFreshChild(ctx, choice)
	if err != nil {
		return nil, err
	}
	request := grantRequestOf(ctx)
	request.mu.Lock()
	plan := request.freshChild
	request.mu.Unlock()
	if plan == nil || plan.conn != c {
		return nil, joinIntentError()
	}
	if err := plan.fetch(ctx, terminal && InputAllowed(ctx)); err != nil {
		return nil, err
	}
	// This final coherent RPC remains outside the physical publication OP.
	// Protected structural and backend generations are both sent in the frame.
	if err := plan.revalidateCohort(ctx); err != nil {
		return nil, err
	}
	args, err = plan.arguments(ctx, args)
	if err != nil {
		return nil, err
	}
	plan.mu.Lock()
	if plan.closed || plan.aborting || plan.retired || plan.attempted || plan.published || plan.busy || !plan.fetched {
		plan.mu.Unlock()
		return nil, joinIntentError()
	}
	plan.busy = true
	args.Secrets = maps.Clone(plan.values)
	creation := cloneSandboxPolicy(plan.creation)
	plan.mu.Unlock()
	defer func() { plan.mu.Lock(); plan.busy = false; plan.mu.Unlock() }()
	owned, release, err := c.beginGrant(ctx, "spawn original fresh member", true)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := plan.authorise(owned); err != nil {
		return nil, err
	}
	source, err := captureHostStateSourceWitness(owned, c)
	if err != nil {
		return nil, err
	}
	witness := &freshChildPublicationWitness{base: ctx, plan: plan, conn: c, creation: creation, args: args, source: source}
	bound := context.WithValue(owned, freshChildPublicationKey{}, witness)
	var child Session
	if err := c.callWithSent(bound, "spawn", args, &child, release); err != nil {
		return nil, err
	}
	return &child, nil
}

func (p *freshChildPlan) markPublicationAttempt() error {
	p.request.mu.Lock()
	err := p.request.lifetimeErrorLocked()
	p.request.mu.Unlock()
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.aborting || p.retired || p.attempted || p.published || !p.busy || !p.fetched {
		return joinIntentError()
	}
	p.attempted = true
	return nil
}
