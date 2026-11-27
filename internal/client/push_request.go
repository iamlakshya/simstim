package client

import (
	"context"
	"reflect"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func samePushIntent(a, b *pushGrantIntent) bool {
	return a != nil && b != nil && a.owner == b.owner && a.host == b.host && a.hostID == b.hostID && a.session == b.session &&
		a.repo == b.repo && a.mode == b.mode && a.once == b.once && a.until.Equal(b.until)
}

func bindRequestPushIntent(ctx context.Context, c *Conn, intent *pushGrantIntent) error {
	if err := requireLocalOperation(ctx, grantLocalForge, grantOpPush); err != nil {
		return err
	}
	if intent == nil || c == nil || c.agentOnly || !intent.matches(c, intent.args()) {
		return grantPurposeError()
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return err
	}
	r := grantRequestOf(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.push != nil {
		if !samePushIntent(r.push, intent) {
			return grantOperationError()
		}
		return nil
	}
	copy := *intent
	r.push = &copy
	return nil
}

func requireRequestPushIntent(ctx context.Context, c *Conn, intent *pushGrantIntent, actual api.PolicyAddArgs) error {
	if err := requireLocalOperation(ctx, grantLocalForge, grantOpPush); err != nil {
		return err
	}
	r := grantRequestOf(ctx)
	r.mu.Lock()
	valid := samePushIntent(r.push, intent) && r.push.matches(c, actual)
	r.mu.Unlock()
	if !valid {
		return grantPurposeError()
	}
	if !intent.until.IsZero() && !time.Now().Before(intent.until) {
		return &UserError{Msg: "the original push grant deadline has passed", Hint: "Start a new request with a current deadline.", Code: "grant_expired", Kind: KindRefused}
	}
	return bindRequestTransportSource(ctx, c)
}

func requirePushOutbound(ctx context.Context, r *GrantRequest, outbound Outbound) error {
	if err := requireLocalOperation(ctx, grantLocalForge, grantOpPush); err != nil {
		return err
	}
	r.mu.Lock()
	intent := r.push
	valid := intent != nil && outbound.Name == intent.repo && (outbound.Kind == OutboundPush && intent.mode == "" ||
		outbound.Kind == OutboundPublicPush && intent.mode == api.PushModePublic)
	r.mu.Unlock()
	if !valid {
		return grantPurposeError()
	}
	return nil
}

type pushPublicationKey struct{}
type pushPublicationWitness struct {
	conn   *Conn
	intent *pushGrantIntent
	plan   *localForgePlan
	name   string
	used   bool
}

func (w *pushPublicationWitness) beforeRequest(ctx context.Context, c *Conn, request proto.Request, arguments any) error {
	actual, ok := arguments.(api.PolicyAddArgs)
	if w == nil || w.used || !ok || c != w.conn || request.ID == 0 || request.Op != "policy.add" ||
		!reflect.DeepEqual(actual, w.intent.args()) {
		return grantPurposeError()
	}
	if err := requireRequestPushIntent(ctx, c, w.intent, actual); err != nil {
		return err
	}
	if w.plan == nil {
		return grantPurposeError()
	}
	if err := w.plan.checkFresh(context.WithValue(ctx, forgeValuePurposeKey{}, w.plan), w.name); err != nil {
		return err
	}
	settings, err := grantRequestOf(ctx).borrowNewSettings(ctx)
	if err != nil {
		return err
	}
	configuration, err := settings.Settings(ctx)
	if err != nil {
		return err
	}
	tier, hostID, err := grantHostTier(ctx, c.Host)
	if err != nil {
		return err
	}
	if hostID != w.intent.hostID {
		return grantIdentityError(c.Host)
	}
	tier = min(tier, w.plan.facts.ceiling, requestCeiling(ctx, tier))
	kind := OutboundPush
	if w.intent.mode == api.PushModePublic {
		kind = OutboundPublicPush
	}
	outbound := Outbound{Kind: kind, Name: w.intent.repo}
	if err := allowRequestGrant(ctx, c.Host, c.HostID(), tier, outbound, configuration); err != nil {
		return err
	}
	if err := recordRequestGrant(ctx, c.Host, c.HostID(), tier, outbound); err != nil {
		return err
	}
	w.used = true
	return nil
}
