package client

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"slices"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func (p *freshChildPlan) authorise(ctx context.Context) error {
	if err := requireGrantOperation(ctx, grantOpFreshChild); err != nil {
		return err
	}
	if grantRequestOf(ctx) != p.request {
		return joinIntentError()
	}
	if err := p.operation.Revalidate(ctx); err != nil {
		return err
	}
	settings, err := p.operation.Settings(ctx)
	if err != nil {
		return err
	}
	return withSandboxReceiptObservation(ctx, p.conn, func(owned context.Context) error {
		if err := p.ancestors.check(owned, p.conn); err != nil {
			return err
		}
		if err := withSandboxFile(owned, p.conn.Host, false, func(file *sandboxFile) error {
			if !bytes.Equal(file.sessions[p.ancestors.parent.SessionID], p.ancestors.parentRaw) ||
				!bytes.Equal(file.sessions[p.ancestors.root.SessionID], p.ancestors.rootRaw) {
				return joinIntentError()
			}
			return nil
		}); err != nil {
			return err
		}
		tiers, err := readTiersFile()
		if err != nil {
			return err
		}
		entry, exists := tiers.Entry(p.conn.Host.Name)
		if !exists || entry.HostID != p.conn.HostID() {
			return grantIdentityError(p.conn.Host)
		}
		tier := min(tiers.Of(p.conn.Host.Name), p.ancestors.parent.Ceiling, p.ancestors.root.Ceiling, requestCeiling(owned, tiers.Of(p.conn.Host.Name)))
		if err := allowSecretNamesAtWall(owned, p.conn.Host, p.conn.HostID(), tier, settings, p.selection.names, p.wall, true); err != nil {
			return err
		}
		return p.operation.Revalidate(owned)
	})
}

func (p *freshChildPlan) revalidateCohort(ctx context.Context) error {
	current, err := p.conn.readFreshChildCohort(ctx, p.choice.Parent)
	if err != nil {
		return err
	}
	if current.RunGeneration != p.cohort.RunGeneration || !sameGrantSnapshot(current.Snapshot, p.cohort.Snapshot) ||
		!reflect.DeepEqual(current.SecretState, p.cohort.SecretState) {
		return joinIntentError()
	}
	return checkFreshCohortDelivery(ctx, current.SecretState.Intent, p.intent)
}

func (p *freshChildPlan) reserve(ctx context.Context) error {
	p.mu.Lock()
	if p.closed || p.aborting || p.retired || p.attempted || p.published || p.busy {
		p.mu.Unlock()
		return joinIntentError()
	}
	if p.creation != nil {
		p.mu.Unlock()
		return nil
	}
	p.busy = true
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.busy = false; p.mu.Unlock() }()
	owned, release, err := p.conn.beginGrant(ctx, "reserve fresh local member", true)
	if err != nil {
		return err
	}
	defer release()
	if err := p.authorise(owned); err != nil {
		return err
	}
	tiers, err := readTiersFile()
	if err != nil {
		return err
	}
	ceiling := min(tiers.Of(p.conn.Host.Name), p.ancestors.parent.Ceiling, p.ancestors.root.Ceiling, requestCeiling(owned, tiers.Of(p.conn.Host.Name)))
	for range 16 {
		id, token, err := p.request.identity()
		if err != nil {
			return err
		}
		exposed := []string{}
		for _, name := range p.selection.names {
			if p.intent.Modes[name] == api.SecretModeExposed {
				exposed = append(exposed, name)
			}
		}
		intent := api.CloneSecretDeliveryIntent(p.intent)
		child := SandboxPolicy{imageSelectionRaw: bytes.Clone(p.ancestors.root.imageSelectionRaw), SessionID: id, HostID: p.conn.HostID(), Ceiling: ceiling, CreationWall: p.wall.level,
			Secrets: append([]string{}, p.selection.names...), Exposed: exposed, CreatedAt: time.Now().UTC(), request: token,
			PolicySources: slices.Clone(p.ancestors.root.PolicySources), DeliveryIntent: &intent,
			lineage: &sandboxLineage{Version: 2, Parent: p.choice.Parent, Root: p.ancestors.root.SessionID,
				Allowed: append([]string{}, p.selection.names...), SelectedKind: p.selection.Kind()}}
		normal, err := normaliseFreshSandboxChild(owned, p.ancestors, child)
		if err != nil {
			return err
		}
		p.mu.Lock()
		p.prospective = normal
		p.mu.Unlock()
		err = reserveFreshSandboxContext(owned, p.conn, p.ancestors, *normal, p)
		if errors.Is(err, errSandboxIDTaken) {
			p.request.retryID()
			continue
		}
		if err != nil {
			return err
		}
		// Keep the ACTUAL canonical row while the same original OP still
		// excludes a replacement. A prospective DTO cannot stand in for it.
		saved, err := LoadSandboxPolicy(owned, p.conn.Host, id)
		if err != nil {
			return err
		}
		if saved == nil || !sameSandboxCreation(saved, normal) || saved.Ceiling != normal.Ceiling ||
			!slices.Equal(saved.Secrets, p.selection.names) || saved.deliveryReceipt != nil || saved.inputOperation != nil ||
			saved.futureDeliveryReceipt || saved.futureInputOperation {
			return sandboxReceiptError()
		}
		p.mu.Lock()
		p.creation = cloneSandboxPolicy(saved)
		p.mu.Unlock()
		return p.operation.Revalidate(owned)
	}
	return joinIntentError()
}

// The lower writer receives only an immutable current-cohort restriction.
// This actual proof is outside its byte callback and makes no RPC or command.
func (p *freshChildPlan) holdFreshSandboxReservation(ctx context.Context, intent *freshSandboxReservationIntent) (*api.SandboxCohortView, func(), error) {
	if err := p.operation.Revalidate(ctx); err != nil {
		return nil, nil, err
	}
	if err := p.ancestors.check(ctx, p.conn); err != nil {
		return nil, nil, err
	}
	p.request.mu.Lock()
	valid := p.request.freshChild == p && p.request.lifetimeErrorLocked() == nil
	p.request.mu.Unlock()
	if !valid {
		return nil, nil, joinIntentError()
	}
	p.mu.Lock()
	if p.closed || p.aborting || p.retired || p.attempted || p.published || !p.busy || p.prospective == nil ||
		intent == nil || intent.snapshot == nil || intent.conn != p.conn || intent.snapshot.conn != p.conn ||
		!bytes.Equal(intent.snapshot.parentRaw, p.ancestors.parentRaw) || !bytes.Equal(intent.snapshot.rootRaw, p.ancestors.rootRaw) ||
		!reflect.DeepEqual(intent.child, p.prospective) || intent.child.lineage.SelectedKind != p.selection.Kind() ||
		!slices.Equal(intent.child.Secrets, p.selection.names) || !reflect.DeepEqual(*intent.child.DeliveryIntent, p.intent) {
		p.mu.Unlock()
		return nil, nil, joinIntentError()
	}
	return p.cohort, p.mu.Unlock, nil
}
