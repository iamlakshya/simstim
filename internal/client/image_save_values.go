package client

import (
	"context"
	"reflect"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
)

type imageSaveValuePurposeKey struct{}

func imageSaveAuthorityError() error {
	return &UserError{Msg: "this image save lost its original session, secret selection or local source",
		Hint: "Start a new image save from the original running microvm session and its local creation record.", Code: "image_source", Kind: KindRefused}
}

func checkImageSavePolicy(p *SandboxPolicy) error {
	if p == nil {
		return imageSaveAuthorityError()
	}
	if p.FutureLineage {
		return futureLineageError()
	}
	if p.FutureDelivery {
		return futureDeliveryError()
	}
	if p.futureDeliveryReceipt || p.futureInputOperation {
		return futureSandboxReceiptError()
	}
	if p.CreationWall != "microvm" || p.inputOperation != nil {
		return imageSaveAuthorityError()
	}
	return nil
}

func requireImageSaveOutbound(ctx context.Context, r *GrantRequest, outbound Outbound, record bool) error {
	o, _ := ctx.Value(imageSaveValuePurposeKey{}).(*imageSaveOperation)
	if o == nil || o.request != r || outbound.Kind != OutboundSecret {
		return grantPurposeError()
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed || !slices.Contains(o.names, outbound.Name) || record && (!o.busy || !o.fetched || !o.publishing) {
		return grantPurposeError()
	}
	return nil
}

// The remote row only narrows locally known creation/receipt names. It cannot
// invent a source, add a selected name or repair an unacknowledged delivery.
func (o *imageSaveOperation) currentSession(ctx context.Context) (*Session, error) {
	if err := o.check(ctx); err != nil {
		return nil, err
	}
	rows, err := o.conn.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	var selected *Session
	for i := range rows {
		if rows[i].ID == o.id {
			if selected != nil {
				return nil, imageSaveAuthorityError()
			}
			selected = &rows[i]
		}
	}
	if selected == nil || selected.Wall == nil || selected.Wall.Level != "microvm" || !selected.Alive {
		return nil, imageSaveAuthorityError()
	}
	if err := o.check(ctx); err != nil {
		return nil, err
	}
	return selected, nil
}

func (o *imageSaveOperation) selectValues(ctx context.Context) error {
	row, err := o.currentSession(ctx)
	if err != nil {
		return err
	}
	sent, err := canonicalGrantNames(row.Secrets)
	if err != nil {
		return imageSaveAuthorityError()
	}
	missing, err := canonicalGrantNames(row.MissingSecrets)
	if err != nil {
		return imageSaveAuthorityError()
	}
	p, err := getSandboxReceiptPolicyContext(ctx, o.conn, o.id)
	if err != nil {
		return err
	}
	if err := checkImageSavePolicy(p); err != nil {
		return err
	}
	if !slices.Equal(sent, localSandboxNames(p)) || (p.DeliveryIntent != nil || p.lineage != nil) && p.deliveryReceipt == nil {
		return imageSaveAuthorityError()
	}
	for _, name := range missing {
		if !slices.Contains(sent, name) {
			return imageSaveAuthorityError()
		}
	}
	names := []string{}
	for _, name := range sent {
		if !slices.Contains(missing, name) {
			names = append(names, name)
		}
	}
	if p.deliveryReceipt != nil {
		if err := validateSandboxDeliveryReceipt(ctx, p.deliveryReceipt); err != nil {
			return err
		}
		if !slices.Equal(sent, p.deliveryReceipt.Allowed) || !slices.Equal(missing, p.deliveryReceipt.Missing) || !slices.Equal(names, p.deliveryReceipt.Supplied) {
			return imageSaveAuthorityError()
		}
	}
	if err := o.conn.requireSecretFeature(len(names) != 0); err != nil {
		return err
	}
	settings, err := o.request.borrowNewSettings(ctx)
	if err != nil {
		return err
	}
	configured, err := settings.Settings(ctx)
	if err != nil {
		return err
	}
	view, err := NormaliseSettingsSecrets(configured)
	if err != nil {
		return err
	}
	for _, name := range names {
		if view.Commands[name] == "" || p.deliveryReceipt != nil && p.deliveryReceipt.State.Intent.Modes[name] == api.SecretModeBound &&
			!reflect.DeepEqual(view.Binds[name], p.deliveryReceipt.State.Intent.Binds[name]) {
			return imageSaveAuthorityError()
		}
	}
	o.mu.Lock()
	o.creation, o.receipt, o.settings, o.view = cloneSandboxPolicy(p), cloneSandboxDeliveryReceipt(p.deliveryReceipt), settings, view
	o.names, o.sent, o.missing = slices.Clone(names), slices.Clone(sent), slices.Clone(missing)
	o.mu.Unlock()
	return nil
}

func (o *imageSaveOperation) restrictSession(ctx context.Context) error {
	row, err := o.currentSession(ctx)
	if err != nil {
		return err
	}
	sent, err := canonicalGrantNames(row.Secrets)
	if err != nil {
		return imageSaveAuthorityError()
	}
	missing, err := canonicalGrantNames(row.MissingSecrets)
	if err != nil || !slices.Equal(sent, o.sent) || !slices.Equal(missing, o.missing) {
		return imageSaveAuthorityError()
	}
	if o.receipt != nil {
		return restrictSavedDeliveryContext(ctx, o.conn, o.creation, o.receipt)
	}
	return nil
}

// checkValuesLocal performs no RPC or command, so it can run immediately
// before the actual common writer while its original short OP is held.
func (o *imageSaveOperation) checkValuesLocal(ctx context.Context) (Tier, error) {
	if err := o.check(ctx); err != nil {
		return TierUnknown, err
	}
	if o.settings == nil || o.creation == nil {
		return TierUnknown, imageSaveAuthorityError()
	}
	if err := o.settings.Revalidate(ctx); err != nil {
		return TierUnknown, err
	}
	settings, err := o.settings.Settings(ctx)
	if err != nil {
		return TierUnknown, err
	}
	tier := TierUnknown
	err = withSandboxReceiptObservation(ctx, o.conn, func(owned context.Context) error {
		p, err := LoadSandboxPolicy(owned, o.conn.Host, o.id)
		if err != nil {
			return err
		}
		if err := checkImageSavePolicy(p); err != nil {
			return err
		}
		if !sameSandboxCreation(p, o.creation) || !reflect.DeepEqual(p.deliveryReceipt, o.receipt) {
			return imageSaveAuthorityError()
		}
		if o.receipt != nil {
			if err := o.receipt.Scope.Source.check(owned, o.conn); err != nil {
				return err
			}
		}
		tiers, err := readTiersFile()
		if err != nil {
			return err
		}
		entry, ok := tiers.Entry(o.conn.Host.Name)
		if !ok || entry.HostID != o.conn.HostID() {
			return grantIdentityError(o.conn.Host)
		}
		tier = min(tiers.Of(o.conn.Host.Name), p.Ceiling, o.creation.Ceiling, requestCeiling(owned, tiers.Of(o.conn.Host.Name)))
		values := context.WithValue(owned, imageSaveValuePurposeKey{}, o)
		if err := allowResumeSecretNames(values, o.conn.Host, o.conn.HostID(), tier, settings, o.names, p); err != nil {
			return err
		}
		return o.settings.Revalidate(values)
	})
	return tier, err
}

func (o *imageSaveOperation) prepare(ctx context.Context, terminal bool) error {
	if err := o.check(ctx); err != nil {
		return err
	}
	o.mu.Lock()
	if o.closed || o.busy || o.attempted {
		o.mu.Unlock()
		return imageSaveAuthorityError()
	}
	o.busy = true
	selected, fetched := o.creation != nil, o.fetched
	o.mu.Unlock()
	defer func() { o.mu.Lock(); o.busy = false; o.mu.Unlock() }()
	if !selected {
		if err := o.selectValues(ctx); err != nil {
			return err
		}
	}
	if err := o.restrictSession(ctx); err != nil {
		return err
	}
	if _, err := o.checkValuesLocal(ctx); err != nil {
		return err
	}
	if fetched {
		return nil
	}
	values := make(map[string]api.Secret, len(o.names))
	for _, name := range o.names {
		if _, err := o.checkValuesLocal(ctx); err != nil {
			clear(values)
			return err
		}
		value, err := runSecret(ctx, name, o.view.Commands[name], terminal && InputAllowed(ctx))
		if err != nil {
			clear(values)
			return err
		}
		values[name] = value
	}
	if _, err := o.checkValuesLocal(ctx); err != nil {
		clear(values)
		return err
	}
	if err := o.restrictSession(ctx); err != nil {
		clear(values)
		return err
	}
	o.mu.Lock()
	o.values, o.fetched = values, true
	o.mu.Unlock()
	return nil
}

func (o *imageSaveOperation) close() error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.busy {
		return agentModeResourceError()
	}
	o.closed = true
	clear(o.values)
	o.values = nil
	o.reviewProgress, o.review = nil, nil
	return nil
}

// Drop carries no values or keep authority. Cancellation still requires the
// exact original local creation/receipt/settings rather than a refreshed row.
func (o *imageSaveOperation) checkCleanupSelection(ctx context.Context) error {
	if o.creation == nil {
		return nil
	}
	if err := o.checkCleanupSettings(ctx); err != nil {
		return err
	}
	return withSandboxReceiptObservation(ctx, o.conn, func(owned context.Context) error {
		p, err := LoadSandboxPolicy(owned, o.conn.Host, o.id)
		if err != nil {
			return err
		}
		if err := checkImageSavePolicy(p); err != nil {
			return err
		}
		if !sameSandboxCreation(p, o.creation) || !reflect.DeepEqual(p.deliveryReceipt, o.receipt) {
			return imageSaveAuthorityError()
		}
		if o.receipt != nil {
			return o.receipt.Scope.Source.check(owned, o.conn)
		}
		return nil
	})
}

func (o *imageSaveOperation) checkCleanupSettings(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	settings := o.settings
	if settings == nil {
		return imageSaveAuthorityError()
	}
	settings.mu.Lock()
	defer settings.mu.Unlock()
	if settings.closed || settings.snapshot == nil {
		return imageSaveAuthorityError()
	}
	// Only the original fixed drop cleanup uses this caller-owned deadline.
	// The copy checks the SAME original FD/path/identity; it cannot borrow
	// decoded Settings, fetch values, renew the retained operation or close it.
	snapshot := *settings.snapshot
	snapshot.ctx = ctx
	return settingsOperationFailure(snapshot.check())
}
