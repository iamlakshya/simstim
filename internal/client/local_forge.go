package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
)

// The closed capability owns selection, value commands, one exact HTTPS GET
// and reduced parsing. No selected name/value/request/header leaves it.
type localForgePlan struct {
	mu                            sync.Mutex
	request                       *GrantRequest
	conn                          *Conn
	intent                        *pushGrantIntent
	operation                     *settingsOperation
	mode                          *agentModeResources
	facts                         localForgeAckFacts
	view                          SecretConfigView
	name                          string
	busy, closed, visibilityValid bool
	visibility                    pushForgeVisibility
	visibilityName                string
}

func (p *localForgePlan) allowsName(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.closed && p.busy && p.name != "" && p.name == name
}

func (p *localForgePlan) close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.busy {
		return agentModeResourceError()
	}
	p.closed = true
	return nil
}

func forgeCandidates(settings Settings, view SecretConfigView) map[string]bool {
	output := map[string]bool{}
	for name, binding := range view.Binds {
		definition := settings.Secrets[name]
		// Only current explicit local forge metadata selects a credential.
		// Another agent's default binding cannot donate this purpose.
		if definition.Hosts == nil || len(definition.Hosts) == 0 || definition.Header == "" || binding.Header != "Authorization" ||
			!slices.Contains(binding.Hosts, net.JoinHostPort("api.github.com", "443")) || view.Kinds[name] != "" {
			continue
		}
		output[name] = true
	}
	return output
}

func localForgePlanFor(ctx context.Context, c *Conn, intent *pushGrantIntent) (*localForgePlan, error) {
	if err := bindRequestPushIntent(ctx, c, intent); err != nil {
		return nil, err
	}
	r := grantRequestOf(ctx)
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
	mode, err := r.borrowMetadataMode(ctx)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.lifetimeErrorLocked(); err != nil {
		return nil, err
	}
	if r.forge == nil {
		r.forge = &localForgePlan{request: r, conn: c, intent: r.push, operation: operation, mode: mode, view: view}
	}
	p := r.forge
	if p.conn != c || p.operation != operation || p.mode != mode || !samePushIntent(p.intent, intent) {
		return nil, grantPurposeError()
	}
	return p, nil
}

func withLocalForgePushVisibility(ctx context.Context, c *Conn, intent *pushGrantIntent, endpoint pushForgeRequest) (pushForgeVisibility, error) {
	if expected, err := pushForgeRequestFor(intent.repo); err != nil || expected != endpoint {
		return pushForgeVisibility{}, grantPurposeError()
	}
	p, err := localForgePlanFor(ctx, c, intent)
	if err != nil {
		return pushForgeVisibility{}, err
	}
	p.mu.Lock()
	if p.closed || p.busy {
		p.mu.Unlock()
		return pushForgeVisibility{}, grantOperationError()
	}
	p.busy = true
	p.visibilityValid = false
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.busy = false; p.mu.Unlock() }()
	settings, err := p.operation.Settings(ctx)
	if err != nil {
		return pushForgeVisibility{}, err
	}
	facts, err := c.localForgeAckFacts(ctx, p.operation, p.mode, intent.session, nil)
	if err != nil {
		return pushForgeVisibility{}, err
	}
	tier, hostID, err := grantHostTier(ctx, c.Host)
	if err != nil {
		return pushForgeVisibility{}, err
	}
	if hostID != facts.scope.HostID {
		return pushForgeVisibility{}, grantIdentityError(c.Host)
	}
	tier = min(tier, facts.ceiling, requestCeiling(ctx, tier))
	candidates := forgeCandidates(settings, p.view)
	for name := range candidates {
		// Pure canonical eligibility filtering does not record an answer.
		if err := allowWithValidatedConfig(c.Host, tier, Outbound{Kind: OutboundSecret, Name: name, Confirmed: true}, &settings, &p.view, nil); err != nil {
			delete(candidates, name)
		}
	}
	if len(candidates) != 0 {
		facts, err = c.localForgeAckFacts(ctx, p.operation, p.mode, intent.session, candidates)
		if err != nil {
			return pushForgeVisibility{}, err
		}
	}
	p.mu.Lock()
	if p.facts.revision != "" && (p.facts.revision != facts.revision || p.facts.creation != facts.creation ||
		!sameReceiptScope(p.facts.scope, facts.scope)) {
		p.mu.Unlock()
		return pushForgeVisibility{}, sandboxReceiptError()
	}
	p.facts, p.name = facts, facts.name
	p.mu.Unlock()
	closed := context.WithValue(ctx, forgeValuePurposeKey{}, p)
	if err := p.checkFresh(closed, ""); err != nil {
		return pushForgeVisibility{}, err
	}
	visibility, err := p.getVisibility(closed, endpoint, "", "")
	if err != nil {
		var status *forgeHTTPStatus
		if !errors.As(err, &status) || status.status != 401 && status.status != 403 && status.status != 404 || facts.name == "" {
			return pushForgeVisibility{}, forgeVisibilityError()
		}
		if err := p.checkFresh(closed, facts.name); err != nil {
			return pushForgeVisibility{}, err
		}
		value, err := runSecret(closed, facts.name, p.view.Commands[facts.name], InputAllowed(ctx))
		if err != nil {
			return pushForgeVisibility{}, err
		}
		if err := p.checkFresh(closed, facts.name); err != nil {
			return pushForgeVisibility{}, err
		}
		visibility, err = p.getVisibility(closed, endpoint, facts.name, value)
		value = ""
		if err != nil {
			return pushForgeVisibility{}, err
		}
		p.mu.Lock()
		p.visibilityName = facts.name
		p.mu.Unlock()
	} else {
		p.mu.Lock()
		p.visibilityName = ""
		p.mu.Unlock()
	}
	if visibility.repo != intent.repo {
		return pushForgeVisibility{}, forgeVisibilityError()
	}
	if err := p.checkFresh(closed, ""); err != nil {
		return pushForgeVisibility{}, err
	}
	p.mu.Lock()
	p.visibility, p.visibilityValid = visibility, true
	p.mu.Unlock()
	return visibility, nil
}

func forgeVisibilityError() error {
	return &UserError{Msg: "the forge did not establish the exact repository's visibility",
		Hint: "Check access and the canonical repository name before granting push access.", Code: "grant_public", Kind: KindRefused}
}

func (p *localForgePlan) checkFresh(ctx context.Context, credential string) error {
	if err := requireRequestPushIntent(ctx, p.conn, p.intent, p.intent.args()); err != nil {
		return err
	}
	if err := p.operation.Revalidate(ctx); err != nil {
		return err
	}
	settings, err := p.operation.Settings(ctx)
	if err != nil {
		return err
	}
	return withSandboxReceiptObservation(ctx, p.conn, func(owned context.Context) error {
		if err := p.facts.scope.Source.check(owned, p.conn); err != nil {
			return err
		}
		if p.facts.scope.HostID != p.conn.HostID() || p.facts.scope.MemberID != p.intent.session {
			return sandboxReceiptError()
		}
		if err := p.checkStateReference(owned); err != nil {
			return err
		}
		tier, hostID, err := grantHostTier(owned, p.conn.Host)
		if err != nil {
			return err
		}
		if hostID != p.facts.scope.HostID {
			return grantIdentityError(p.conn.Host)
		}
		tier = min(tier, p.facts.ceiling, requestCeiling(owned, tier))
		kind := OutboundPush
		if p.intent.mode == api.PushModePublic {
			kind = OutboundPublicPush
		}
		if err := allowRequestGrant(owned, p.conn.Host, hostID, tier, Outbound{Kind: kind, Name: p.intent.repo}, settings); err != nil {
			return err
		}
		if credential != "" {
			if credential != p.facts.name || !p.allowsName(credential) {
				return grantPurposeError()
			}
			if err := allowRequestGrant(owned, p.conn.Host, hostID, tier, Outbound{Kind: OutboundSecret, Name: credential}, settings); err != nil {
				return err
			}
		}
		return p.operation.Revalidate(owned)
	})
}

func (p *localForgePlan) checkStateReference(ctx context.Context) error {
	return withStateTransaction(ctx, []string{"hosts", HostFileKey(p.conn.Host.Name)}, "sandboxes.json", false, false, func(tx *stateFileTxn) error {
		if tx.source == nil {
			return sandboxReceiptError()
		}
		return p.facts.stateReference.CheckFile(tx.root, tx.name, tx.source, 0600)
	})
}

func (*localForgePlan) Format(state fmt.State, _ rune) { _, _ = io.WriteString(state, api.Redacted) }
