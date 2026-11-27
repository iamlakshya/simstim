package client

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/iamlakshya/simstim/internal/api"
)

type legacyPasteWriteKey struct{}

// This proof exists only between the actual Upload final gates and its ONE
// canonical ledger call. It is not inferred from a grant enum or request.
type legacyPasteWriteProof struct {
	conn    *Conn
	session string
	tier    Tier
	active  atomic.Bool
}

func withLegacyPasteWriteProof(ctx context.Context, c *Conn, session string, tier Tier) (context.Context, func()) {
	proof := &legacyPasteWriteProof{conn: c, session: session, tier: tier}
	proof.active.Store(true)
	return context.WithValue(ctx, legacyPasteWriteKey{}, proof), sync.OnceFunc(func() { proof.active.Store(false) })
}

func legacyProducerConn(ctx context.Context) (*Conn, bool, error) {
	o, _ := ctx.Value(hostStateObservationKey{}).(*HostStateObservation)
	if o == nil || !o.hasLegacyBinding() {
		return nil, false, nil
	}
	c, err := o.legacyTransport(ctx)
	return c, true, err
}

func prepareLegacyLedgerContext(ctx context.Context, h Host, hostID string, outbound Outbound) (context.Context, func(), error) {
	c, legacy, err := legacyProducerConn(ctx)
	if err != nil {
		return ctx, nil, err
	}
	if !legacy {
		return ctx, func() {}, nil
	}
	if hostID != "" || !SameEntry(c.Host, h) {
		return ctx, nil, grantIdentityError(h)
	}
	intent := legacyStateWriteIntent{conn: c, outbound: outbound}
	if outbound.Kind == OutboundPaste {
		intent.kind = legacyPasteRecord
		intent.paste, _ = ctx.Value(legacyPasteWriteKey{}).(*legacyPasteWriteProof)
		if intent.paste != nil {
			intent.session = intent.paste.session
		}
	} else {
		intent.request = grantRequestOf(ctx)
		if intent.request == nil {
			return ctx, nil, grantOperationError()
		}
		switch requestGrantOperation(intent.request) {
		case grantOpNew:
			intent.kind = legacyNewCredentialRecord
		case grantOpResume:
			intent.kind = legacyResumeCredentialRecord
			intent.request.mu.Lock()
			if intent.request.legacyResume != nil {
				intent.session = intent.request.legacyResume.id
			}
			intent.request.mu.Unlock()
		default:
			return ctx, nil, grantOperationError()
		}
	}
	return prepareLegacyStateWriteContext(ctx, intent)
}

func prepareLegacyNoneContext(ctx context.Context, h Host, row SandboxPolicy) (context.Context, func(), error) {
	c, legacy, err := legacyProducerConn(ctx)
	if err != nil {
		return ctx, nil, err
	}
	if !legacy {
		return ctx, func() {}, nil
	}
	if !SameEntry(c.Host, h) {
		return ctx, nil, grantIdentityError(h)
	}
	return prepareLegacyStateWriteContext(ctx, legacyStateWriteIntent{kind: legacyNewNoneReceipt, conn: c,
		request: grantRequestOf(ctx), session: row.SessionID, none: &row})
}

// This is the ONE semantic predicate used before prepare and before lower
// admission. It runs outside byte callbacks and never mutates a parsed row.
func validateLegacyStateWriteIntent(ctx context.Context, intent *legacyStateWriteIntent) error {
	if err := requireRemoteGrantRequest(ctx); err != nil {
		return err
	}
	if intent == nil || intent.conn == nil || intent.conn.HostID() != "" || intent.conn.agentOnly {
		return grantOperationError()
	}
	if err := checkAcceptedHostTransport(intent.conn); err != nil {
		return err
	}
	if intent.kind == legacyPasteRecord {
		return validateLegacyPasteWrite(ctx, intent)
	}
	r := grantRequestOf(ctx)
	if r == nil || intent.request != r {
		return grantOperationError()
	}
	r.mu.Lock()
	selected := r.selection != nil && r.selection.host == intent.conn.Host.Name &&
		sameHostTransportSource(r.selection.source, intent.conn.hostTransport.source)
	ordinary := r.purpose == grantRemote && !r.SSHAgent && r.review == nil && r.join == nil && r.resume == nil && !r.resumeGroup && r.operationUntrusted == r.Untrusted
	r.mu.Unlock()
	if !selected || !ordinary {
		return grantOperationError()
	}
	switch intent.kind {
	case legacyNewCredentialRecord, legacyNewNoneReceipt:
		return validateLegacyNewWrite(ctx, intent)
	case legacyResumeCredentialRecord:
		return validateLegacyResumeWrite(ctx, intent)
	default:
		return grantOperationError()
	}
}

func validateLegacyNewWrite(ctx context.Context, intent *legacyStateWriteIntent) error {
	r := intent.request
	r.mu.Lock()
	var before *newGrantIntent
	if r.newIntent != nil {
		copy := *r.newIntent
		copy.names, copy.exposed = slices.Clone(copy.names), slices.Clone(copy.exposed)
		before = &copy
	}
	op := r.operation
	id, token := r.id, r.token
	r.mu.Unlock()
	if op != grantOpNew || before == nil || before.hostID != "" || before.sshAgent || before.wall != "none" || !SameEntry(before.host, intent.conn.Host) {
		return grantOperationError()
	}
	_, ceiling, err := intent.conn.newGrantTier(ctx, before.names, false)
	if err != nil {
		return err
	}
	if intent.kind == legacyNewCredentialRecord {
		if intent.outbound.Kind != OutboundSecret || !slices.Contains(before.names, intent.outbound.Name) {
			return grantOperationError()
		}
		return nil
	}
	p := intent.none
	if p == nil || !intent.conn.HasFeature("session.id") || p.SessionID != intent.session || p.SessionID != id || token == "" || p.request != token ||
		p.HostID != "" || p.Ceiling != ceiling || p.CreationWall != "none" || p.SSHAgent || p.FutureLineage || p.lineage != nil ||
		p.FutureDelivery || p.DeliveryIntent != nil || !slices.Equal(p.Secrets, before.names) || !slices.Equal(p.Exposed, before.exposed) {
		return grantOperationError()
	}
	if len(p.PolicySources) != 0 {
		pin, err := api.DecodePolicySourcePin(p.PolicySources)
		if err != nil || pin == nil || pin.ClientInputs != nil {
			return policySourceStateError()
		}
	}
	return nil
}

func validateLegacyResumeWrite(ctx context.Context, intent *legacyStateWriteIntent) error {
	r := intent.request
	r.mu.Lock()
	var before *legacyResumeGrantIntent
	if r.legacyResume != nil {
		copy := *r.legacyResume
		copy.names = slices.Clone(copy.names)
		copy.creation = cloneSandboxPolicy(copy.creation)
		before = &copy
	}
	op := r.operation
	r.mu.Unlock()
	if op != grantOpResume || before == nil || before.hostID != "" || before.id != intent.session || !SameEntry(before.host, intent.conn.Host) ||
		intent.outbound.Kind != OutboundSecret || !slices.Contains(before.names, intent.outbound.Name) {
		return grantOperationError()
	}
	if err := checkUncheckedResumeSelection(ctx, intent.conn, before.creation); err != nil {
		return err
	}
	_, err := intent.conn.checkResumeGrantNames(ctx, before.id, before.names)
	return err
}

func checkUncheckedResumeSelection(ctx context.Context, c *Conn, p *SandboxPolicy) error {
	if c.HostID() != "" {
		return nil
	}
	if request := grantRequestOf(ctx); request != nil && request.SSHAgent {
		return grantIdentityError(c.Host)
	}
	if c.HasFeature(api.FeatureSpawn) || c.HasFeature(api.FeatureSandboxSnapshot) {
		return grantIdentityError(c.Host)
	}
	if p != nil && (p.HostID != "" || p.SSHAgent || p.FutureLineage || p.lineage != nil || p.FutureDelivery || p.DeliveryIntent != nil || (p.CreationWall != "" && p.CreationWall != "none" && p.CreationWall != "process")) {
		return grantIdentityError(c.Host)
	}
	return nil
}

func validateLegacyPasteWrite(ctx context.Context, intent *legacyStateWriteIntent) error {
	p := intent.paste
	if p == nil || !p.active.Load() || p.conn != intent.conn || p.session != intent.session || intent.request != nil ||
		intent.outbound.Kind != OutboundPaste || intent.outbound.Name != "" {
		return grantOperationError()
	}
	tier, creation, err := EffectiveSessionTier(ctx, p.conn.Host, p.session, p.conn.HostID())
	if err != nil {
		return err
	}
	if err := checkPasteCreationPolicy(p.conn, creation); err != nil {
		return err
	}
	if tier != p.tier {
		return grantOperationError()
	}
	return Allow(p.conn.Host, tier, intent.outbound, LoadSettings())
}

func checkLegacyNoneSchema(ctx context.Context, schema int) error {
	permit, _ := ctx.Value(legacyStateWriteKey{}).(*legacyStateWritePermit)
	if permit != nil && permit.intent.kind == legacyNewNoneReceipt && schema >= 4 {
		return grantOperationError()
	}
	return nil
}

func checkPasteCreationPolicy(c *Conn, p *SandboxPolicy) error {
	if p == nil {
		return nil
	}
	if p.FutureLineage || p.FutureDelivery {
		return grantIdentityError(c.Host)
	}
	if c.HostID() == "" && (p.HostID != "" || p.SSHAgent || p.lineage != nil || p.DeliveryIntent != nil ||
		(p.CreationWall != "" && p.CreationWall != "none" && p.CreationWall != "process")) {
		return grantIdentityError(c.Host)
	}
	return nil
}
