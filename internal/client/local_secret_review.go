package client

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

// localReviewValuePlan is a fresh local-only capability. The original local
// ACK receipt supplies its name domain. Saved remote context only restricts it.
// It never records an exposure, sends values, arms a backend or writes history.
type localReviewValuePlan struct {
	connection *Conn
	creation   *SandboxPolicy
	receipt    *sandboxDeliveryReceipt
	operation  *settingsOperation
	names      []string
	view       SecretConfigView
}

// ReviewPublicStandIns returns original acknowledged public metadata, or an
// empty set positively proved by the original pre-wall schema. It executes no
// command or RPC. A row or later remote context cannot add a public stand-in.
func (c *Conn) ReviewPublicStandIns(ctx context.Context, id string) ([]api.Secret, error) {
	p, err := getSandboxReceiptPolicyContext(ctx, c, id)
	if err != nil {
		return nil, err
	}
	if p == nil || p.futureDeliveryReceipt {
		return nil, reviewAuthorityError()
	}
	if p.deliveryReceipt == nil {
		if err := c.checkOriginalLegacyNonePublicSet(ctx, id, p); err != nil {
			return nil, err
		}
		return []api.Secret{}, ctx.Err()
	}
	r := p.deliveryReceipt
	if err := validateSandboxDeliveryReceipt(ctx, r); err != nil {
		return nil, err
	}
	var out []api.Secret
	for _, name := range r.Allowed {
		if state, ok := r.State.Primary[name]; ok && state.Mode == api.SecretModeBound {
			out = append(out, state.Sentinel)
		}
		for _, state := range r.State.Minted[name] {
			out = append(out, state.Sentinel)
		}
		for _, state := range r.State.Retired[name] {
			out = append(out, state.Sentinel)
		}
	}
	return out, ctx.Err()
}

func fetchAcknowledgedReviewSecrets(ctx context.Context, original Host, id string, sent, missing []string, terminal bool) (_ map[string]api.Secret, err error) {
	// Refuse before dialing or opening a command if the original local writer
	// has never acknowledged this publication. Row names cannot repair that.
	before, err := LoadSandboxPolicy(ctx, original, id)
	if err != nil {
		return nil, err
	}
	if before == nil || before.futureDeliveryReceipt || before.deliveryReceipt == nil {
		return nil, reviewAuthorityError()
	}
	c, err := Dial(ctx, original, true)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	plan, err := prepareLocalReviewValuePlan(ctx, c, id, sent, missing)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, plan.operation.Close()) }()
	return plan.read(ctx, terminal)
}

func prepareLocalReviewValuePlan(ctx context.Context, c *Conn, id string, sent, missing []string) (_ *localReviewValuePlan, err error) {
	if r := grantRequestOf(ctx); r == nil || requestGrantPurpose(r) != grantLocalReview {
		return nil, grantPurposeError()
	}
	p, err := getSandboxReceiptPolicyContext(ctx, c, id)
	if err != nil {
		return nil, err
	}
	if p == nil || p.FutureLineage || p.FutureDelivery || p.futureDeliveryReceipt || p.deliveryReceipt == nil {
		return nil, reviewAuthorityError()
	}
	receipt := cloneSandboxDeliveryReceipt(p.deliveryReceipt)
	if err := validateSandboxDeliveryReceipt(ctx, receipt); err != nil {
		return nil, err
	}
	if !slices.Equal(receipt.Allowed, sent) || !slices.Equal(receipt.Missing, missing) {
		return nil, reviewAuthorityError()
	}
	r := grantRequestOf(ctx)
	if _, err := r.bindReviewIntent(c.Host, c.HostID(), id, sent, missing, p); err != nil {
		return nil, err
	}
	r.mu.Lock()
	if r.review.receiptRevision != "" && r.review.receiptRevision != receipt.Revision {
		r.mu.Unlock()
		return nil, reviewAuthorityError()
	}
	r.review.receiptRevision = receipt.Revision
	r.mu.Unlock()
	operation, err := openSettingsOperation(ctx)
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			err = errors.Join(err, operation.Close())
		}
	}()
	settings, err := operation.Settings(ctx)
	if err != nil {
		return nil, err
	}
	view, err := NormaliseSettingsSecrets(settings)
	if err != nil {
		return nil, err
	}
	plan := &localReviewValuePlan{connection: c, creation: cloneSandboxPolicy(p), receipt: receipt,
		operation: operation, names: slices.Clone(receipt.Supplied), view: view}
	for _, name := range plan.names {
		if receipt.State.Intent.Modes[name] == api.SecretModeBound && !reflect.DeepEqual(view.Binds[name], receipt.State.Intent.Binds[name]) {
			return nil, reviewAuthorityError()
		}
		if view.Commands[name] == "" {
			return nil, reviewAuthorityError()
		}
	}
	if err := plan.restrictSavedContext(ctx); err != nil {
		return nil, err
	}
	if err := plan.checkLocal(ctx); err != nil {
		return nil, err
	}
	keep = true
	return plan, nil
}

func (p *localReviewValuePlan) checkLocal(ctx context.Context) error {
	if err := p.operation.Revalidate(ctx); err != nil {
		return err
	}
	settings, err := p.operation.Settings(ctx)
	if err != nil {
		return err
	}
	return withSandboxReceiptObservation(ctx, p.connection, func(owned context.Context) error {
		if err := bindRequestTransportSource(owned, p.connection); err != nil {
			return err
		}
		if err := p.receipt.Scope.Source.check(owned, p.connection); err != nil {
			return err
		}
		tiers, err := readTiersFile()
		if err != nil {
			return err
		}
		entry, ok := tiers.Entry(p.connection.Host.Name)
		if !ok || entry.HostID != p.receipt.Scope.HostID {
			return reviewAuthorityError()
		}
		tier := min(tiers.Of(p.connection.Host.Name), p.creation.Ceiling)
		for _, name := range p.names {
			if err := allowRequestGrant(owned, p.connection.Host, p.connection.HostID(), tier, Outbound{Kind: OutboundSecret, Name: name}, settings); err != nil {
				return err
			}
		}
		return nil
	})
}

// This read-only saved path works while the sandbox is stopped. It neither
// starts an export helper nor wakes/arms the sandbox or creates a receipt.
func (p *localReviewValuePlan) restrictSavedContext(ctx context.Context) error {
	return restrictSavedDeliveryContext(ctx, p.connection, p.creation, p.receipt)
}

// This is a read-only restriction, not a value purpose or a name grant.
// Both callers already own their distinct local selection and permission.
func restrictSavedDeliveryContext(ctx context.Context, c *Conn, creation *SandboxPolicy, r *sandboxDeliveryReceipt) error {
	if !c.HasFeature(api.FeatureSandboxReviewContext) {
		return c.grantHelperOld("read saved sandbox context without starting it")
	}
	var raw json.RawMessage
	if err := c.Call(ctx, api.SandboxReviewContextOp, api.SandboxSnapshotArgs{ID: creation.SessionID}, &raw); err != nil {
		return err
	}
	view, err := api.DecodeSandboxCohort(raw, creation.SessionID)
	if err != nil {
		return reviewAuthorityError()
	}
	if err := egress.ValidateSavedPublicSessionState(view.SecretState); err != nil {
		return reviewAuthorityError()
	}
	if view.Snapshot.Primary != r.Scope.RootID || view.RunGeneration != r.Scope.RunGeneration || view.SecretState.Generation != r.Scope.BackendGeneration {
		return reviewAuthorityError()
	}
	for _, name := range r.Allowed {
		if view.SecretState.Intent.Modes[name] != r.State.Intent.Modes[name] ||
			!reflect.DeepEqual(view.SecretState.Intent.Binds[name], r.State.Intent.Binds[name]) {
			return reviewAuthorityError()
		}
		if r.State.Intent.Modes[name] == api.SecretModeBound {
			a, b := view.SecretState.Primary[name], r.State.Primary[name]
			// Stopping clears live arming; the original public identity/shape
			// and last applied namespace remain the restriction for review.
			a.Armed, b.Armed = false, false
			if !reflect.DeepEqual(a, b) {
				return reviewAuthorityError()
			}
		}
	}
	return nil
}

func (p *localReviewValuePlan) read(ctx context.Context, terminal bool) (map[string]api.Secret, error) {
	out := make(map[string]api.Secret, len(p.names))
	// All terminal questions occur before the first command. Every command
	// then rechecks the same original Source/settings and current tier gates.
	if err := p.checkLocal(ctx); err != nil {
		return nil, err
	}
	for _, name := range p.names {
		if err := p.checkLocal(ctx); err != nil {
			return nil, err
		}
		value, err := runSecret(ctx, name, p.view.Commands[name], terminal)
		if err != nil {
			return nil, err
		}
		out[name] = value
	}
	if err := p.checkLocal(ctx); err != nil {
		return nil, err
	}
	if err := p.restrictSavedContext(ctx); err != nil {
		return nil, err
	}
	return out, nil
}
