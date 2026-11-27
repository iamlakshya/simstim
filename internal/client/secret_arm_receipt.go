package client

import (
	"context"
	"maps"
	"reflect"
	"slices"
	"sort"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

// This follows only the same actual correlated arm ACK. No read-only
// readiness result, DTO or earlier operation can construct its proof.
func (p *armSecretsPlanV1) storeAcknowledgedReceiptV1(ctx context.Context, c *Conn, state *api.SessionSecretState) error {
	p.mu.Lock()
	valid := p.conn == c && p.attempted && p.written && p.acknowledged && p.requestID != 0 && p.wire != ([32]byte{}) && !p.closed && reflect.DeepEqual(p.ack, state)
	existing := cloneSandboxDeliveryReceipt(p.receipt)
	p.mu.Unlock()
	if !valid {
		return sandboxReceiptError()
	}
	if existing == nil {
		allowed, err := canonicalGrantNames(localSandboxNames(p.creation))
		if err != nil {
			return err
		}
		allowed = append([]string{}, allowed...)
		domain := make(map[string]bool, len(allowed))
		for _, name := range allowed {
			domain[name] = true
		}
		local, err := egress.ProjectLocalSessionSecretStateV1(state, domain)
		if err != nil {
			return err
		}
		p.mu.Lock()
		delta := append([]string{}, secretMapNames(p.values)...)
		trusted := []string{}
		if p.judgeNeed {
			trusted = append(trusted, p.judge.name)
		}
		before := p.before.Clone()
		source := p.source.clone()
		p.mu.Unlock()
		suppliedSet := map[string]bool{}
		previous := ""
		if prior := p.creation.deliveryReceipt; prior != nil {
			previous = prior.Revision
			for _, name := range prior.Supplied {
				suppliedSet[name] = true
			}
		}
		for _, name := range delta {
			suppliedSet[name] = true
		}
		supplied := slices.Sorted(maps.Keys(suppliedSet))
		if supplied == nil {
			supplied = []string{}
		}
		missing := []string{}
		for _, name := range supplied {
			if !domain[name] {
				return sandboxReceiptError()
			}
		}
		for _, name := range allowed {
			if !suppliedSet[name] {
				missing = append(missing, name)
			}
		}
		sort.Strings(trusted)
		revision, err := newLocalReceiptID()
		if err != nil {
			return err
		}
		publication, err := newLocalReceiptID()
		if err != nil {
			return err
		}
		receipt := &sandboxDeliveryReceipt{Version: 2, Revision: revision, Previous: previous, Creation: p.creation.request, Publication: publication, Operation: "arm",
			Scope:   sandboxReceiptScope{Host: c.Host.Name, Source: source, HostID: c.HostID(), MemberID: p.id, RootID: before.Snapshot.Primary, RunGeneration: before.RunGeneration, BackendGeneration: local.Generation},
			Allowed: allowed, Supplied: supplied, Missing: missing, State: local, ArmSupplied: delta, TrustedArmSupplied: trusted}
		p.mu.Lock()
		if p.receipt == nil {
			p.receipt = cloneSandboxDeliveryReceipt(receipt)
		}
		existing = cloneSandboxDeliveryReceipt(p.receipt)
		p.mu.Unlock()
	}
	p.mu.Lock()
	p.receiptAttempted = true
	p.mu.Unlock()
	if err := storeArmDeliveryReceiptV2(ctx, c, p.creation, existing.Previous, existing, p); err != nil {
		return err
	}
	p.mu.Lock()
	p.receiptStored = true
	p.mu.Unlock()
	return nil
}

func (p *armSecretsPlanV1) checkSandboxReceiptWrite(ctx context.Context, intent *sandboxReceiptWriteIntent) error {
	p.mu.Lock()
	valid := intent != nil && intent.kind == armPublicationReceipt && intent.conn == p.conn && p.attempted && p.written && p.acknowledged && p.requestID != 0 && p.wire != ([32]byte{}) && p.receipt != nil && !p.closed &&
		intent.expectedRevision == p.receipt.Previous && sameSandboxCreation(intent.original, p.creation) && reflect.DeepEqual(intent.delivery, p.receipt)
	source := p.source.clone()
	p.mu.Unlock()
	if !valid || grantRequestOf(ctx) != p.request {
		return sandboxReceiptError()
	}
	p.request.mu.Lock()
	original := p.request.armSecrets == p
	life := p.request.lifetimeErrorLocked()
	p.request.mu.Unlock()
	if !original || life != nil {
		return sandboxReceiptError()
	}
	if err := p.operation.Revalidate(ctx); err != nil {
		return err
	}
	if err := bindRequestTransportSource(ctx, p.conn); err != nil {
		return err
	}
	return source.check(ctx, p.conn)
}
