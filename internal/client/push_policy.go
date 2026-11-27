package client

import (
	"context"

	"github.com/iamlakshya/simstim/internal/api"
)

// CheckPushGrant previews one LOCAL flag intent through the SAME H5 gates.
// A later actual add repeats the visibility check and final source/intent
// check; this read grants no reusable authorization and records no exposure.
func (c *Conn) CheckPushGrant(ctx context.Context, args api.PolicyAddArgs, publicFlag bool) error {
	if err := requireLocalOperation(ctx, grantLocalForge, grantOpPush); err != nil {
		return err
	}
	intent, err := newPushGrantIntent(c, args, publicFlag)
	if err != nil {
		return err
	}
	return c.checkPushGrantIntent(ctx, intent)
}

// AllowPushPolicy adds only the original positive local push intent after a
// fresh closed visibility query. Source locks cover only its actual first
// mutation frame; questions, credential commands and HTTPS run outside them.
func (c *Conn) AllowPushPolicy(ctx context.Context, args api.PolicyAddArgs, publicFlag bool) (api.Rule, error) {
	var result api.Rule
	if err := requireLocalOperation(ctx, grantLocalForge, grantOpPush); err != nil {
		return result, err
	}
	intent, err := newPushGrantIntent(c, args, publicFlag)
	if err != nil {
		return result, err
	}
	if err := c.checkPushGrantIntent(ctx, intent); err != nil {
		return result, err
	}
	r := grantRequestOf(ctx)
	r.mu.Lock()
	plan := r.forge
	r.mu.Unlock()
	if plan == nil {
		return result, grantPurposeError()
	}
	plan.mu.Lock()
	if plan.closed || plan.busy || !plan.visibilityValid || plan.visibility.repo != intent.repo {
		plan.mu.Unlock()
		return result, grantOperationError()
	}
	plan.busy = true
	plan.mu.Unlock()
	defer func() { plan.mu.Lock(); plan.busy = false; plan.mu.Unlock() }()
	owned, release, err := c.beginLocalGrant(ctx, grantLocalForge, grantOpPush, "publish exact push allowance")
	if err != nil {
		return result, err
	}
	defer release()
	actual := intent.args()
	if err := requireRequestPushIntent(owned, c, intent, actual); err != nil {
		return result, err
	}
	closed := context.WithValue(owned, forgeValuePurposeKey{}, plan)
	plan.mu.Lock()
	name := plan.visibilityName
	plan.mu.Unlock()
	if err := plan.checkFresh(closed, name); err != nil {
		return result, err
	}
	witness := &pushPublicationWitness{conn: c, intent: intent, plan: plan, name: name}
	closed = context.WithValue(closed, pushPublicationKey{}, witness)
	err = c.callWithSent(closed, "policy.add", actual, &result, release)
	return result, err
}
