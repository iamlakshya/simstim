package client

import (
	"context"
	"fmt"
	"io"
	"maps"
	"reflect"
	"slices"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

// Only the actual original New planner selects these configured names.
// The private values never enter generic Secrets, Environment or a guest.
type newNamedSSHKeyPlan struct {
	mu        sync.Mutex
	conn      *Conn
	stamp     *hostTransportStamp
	operation *settingsOperation
	ceiling   Tier
	names     []string
	values    map[string]api.Secret
	fetched   bool
}

func (*newNamedSSHKeyPlan) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }

func namedSSHKeySourceError() error {
	return &UserError{Msg: "the named SSH keys differ from the original local cold-start selection", Hint: "Choose configured ssh-key names in sandbox.ssh_keys and start a new sandbox request.", Code: "ssh_key_source", Kind: KindRefused}
}

func prepareNewNamedSSHKeys(ctx context.Context, p *newSecretPlan, a NewArgs) (*newNamedSSHKeyPlan, error) {
	policy, _, err := egress.MergeLayers(StartPolicyLayers(a))
	if err != nil {
		return nil, policyError(err)
	}
	names, err := canonicalGrantNames(policy.Sandbox.SSHKeys)
	if err != nil {
		return nil, namedSSHKeySourceError()
	}
	r := grantRequestOf(ctx)
	if r == nil {
		return nil, grantOperationError()
	}
	tier, hid, err := grantHostTier(ctx, p.conn.Host)
	if err != nil {
		return nil, err
	}
	if hid != p.conn.HostID() {
		return nil, grantIdentityError(p.conn.Host)
	}
	n := &newNamedSSHKeyPlan{names: names, values: map[string]api.Secret{}, conn: p.conn, stamp: p.conn.hostTransport, operation: p.operation, ceiling: min(tier, requestCeiling(ctx, tier))}
	r.mu.Lock()
	before := r.namedNew
	if before == nil {
		r.namedNew = n
		before = n
	}
	r.mu.Unlock()
	if before.conn != p.conn || before.stamp != p.conn.hostTransport || before.operation != p.operation || !slices.Equal(before.names, names) {
		return nil, namedSSHKeySourceError()
	}
	n = before
	if len(names) > 0 {
		if a.Wall == "" || a.Wall == "none" || a.SSHAgent {
			return nil, namedSSHKeySourceError()
		}
		if !p.conn.HasFeature(api.FeatureNamedSSHKeysV1) || !p.conn.HasFeature("session.id") || !p.conn.HasFeature(api.FeatureBoundSecrets) {
			return nil, p.conn.grantHelperOld("receive original named SSH keys")
		}
	}
	if err := n.check(ctx, p); err != nil {
		return nil, err
	}
	return n, nil
}

func (n *newNamedSSHKeyPlan) check(ctx context.Context, p *newSecretPlan) error {
	if n == nil || len(n.names) == 0 {
		return nil
	}
	if err := requireGrantOperation(ctx, grantOpNew); err != nil {
		return err
	}
	r := grantRequestOf(ctx)
	r.mu.Lock()
	original := r.namedNew == n
	r.mu.Unlock()
	if !original {
		return namedSSHKeySourceError()
	}
	if p == nil || p.conn == nil || n.conn != p.conn || n.stamp != p.conn.hostTransport || n.operation != p.operation || !ValidHostID(p.conn.HostID()) {
		return namedSSHKeySourceError()
	}
	if err := bindRequestTransportSource(ctx, p.conn); err != nil {
		return err
	}
	settings, err := p.operation.Settings(ctx)
	if err != nil {
		return err
	}
	view, err := NormaliseSettingsSecrets(settings)
	if err != nil {
		return err
	}
	tier, hid, err := grantHostTier(ctx, p.conn.Host)
	if err != nil {
		return err
	}
	if hid != p.conn.HostID() {
		return grantIdentityError(p.conn.Host)
	}
	tier = min(tier, n.ceiling, requestCeiling(ctx, tier))
	if p.creation != nil {
		saved, err := LoadSandboxPolicy(ctx, p.conn.Host, p.creation.SessionID)
		if err != nil {
			return err
		}
		if !sameSandboxCreation(saved, p.creation) {
			return namedSSHKeySourceError()
		}
		tier = min(tier, p.creation.Ceiling)
	}
	for _, name := range n.names {
		if view.Kinds[name] != "ssh-key" || view.Commands[name] == "" || slices.Contains(p.selection.names, name) {
			return namedSSHKeySourceError()
		}
		if err := allowRequestGrant(ctx, p.conn.Host, hid, tier, Outbound{Kind: OutboundSSHKey, Name: name}, settings); err != nil {
			return err
		}
	}
	return p.operation.Revalidate(ctx)
}

func (n *newNamedSSHKeyPlan) fetch(ctx context.Context, p *newSecretPlan, terminal bool) error {
	if n == nil || len(n.names) == 0 {
		return nil
	}
	if err := n.check(ctx, p); err != nil {
		return err
	}
	settings, err := p.operation.Settings(ctx)
	if err != nil {
		return err
	}
	view, err := NormaliseSettingsSecrets(settings)
	if err != nil {
		return err
	}
	for _, name := range n.names {
		if err := n.check(ctx, p); err != nil {
			return err
		}
		n.mu.Lock()
		_, done := n.values[name]
		n.mu.Unlock()
		if done {
			continue
		}
		value, err := runSecret(ctx, name, view.Commands[name], terminal && InputAllowed(ctx))
		if err != nil {
			return err
		}
		if err := n.check(ctx, p); err != nil {
			return err
		}
		n.mu.Lock()
		n.values[name] = value
		n.mu.Unlock()
	}
	n.mu.Lock()
	n.fetched = true
	n.mu.Unlock()
	return nil
}

func (n *newNamedSSHKeyPlan) arguments(original *api.NamedSSHKeysV1) (*api.NamedSSHKeysV1, error) {
	if n == nil || len(n.names) == 0 {
		if original != nil {
			return nil, namedSSHKeySourceError()
		}
		return nil, nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.fetched {
		if original != nil {
			return nil, namedSSHKeySourceError()
		}
		return nil, nil
	}
	expected := &api.NamedSSHKeysV1{Version: 1, Names: slices.Clone(n.names), Values: maps.Clone(n.values)}
	if !expected.Valid() || original != nil && !reflect.DeepEqual(original, expected) {
		return nil, namedSSHKeySourceError()
	}
	return expected, nil
}

func (n *newNamedSSHKeyPlan) beforePhysical(ctx context.Context, p *newSecretPlan, sent *api.NamedSSHKeysV1) error {
	if n != nil && len(n.names) > 0 && (p == nil || p.creation == nil) {
		return namedSSHKeySourceError()
	}
	expected, err := n.arguments(sent)
	if err != nil {
		return err
	}
	if n == nil || len(n.names) == 0 {
		return nil
	}
	if expected == nil {
		return namedSSHKeySourceError()
	}
	if err := n.check(ctx, p); err != nil {
		return err
	}
	tier, _, err := grantHostTier(ctx, p.conn.Host)
	if err != nil {
		return err
	}
	tier = min(tier, n.ceiling, p.creation.Ceiling, requestCeiling(ctx, tier))
	for _, name := range n.names {
		if err := recordRequestGrant(ctx, p.conn.Host, p.conn.HostID(), tier, Outbound{Kind: OutboundSSHKey, Name: name}); err != nil {
			return err
		}
	}
	return n.check(ctx, p)
}

func (n *newNamedSSHKeyPlan) checkAcknowledged(view *api.NamedSSHKeyViewV1) error {
	if n == nil || len(n.names) == 0 {
		return nil
	}
	if view == nil || !view.Valid() || !slices.Equal(view.Installed, n.names) {
		return namedSSHKeySourceError()
	}
	return nil
}

func (n *newNamedSSHKeyPlan) close() {
	if n != nil {
		n.mu.Lock()
		defer n.mu.Unlock()
		clear(n.values)
		n.values = nil
		n.fetched = false
	}
}

func refuseUnwalledNamedSSHKeys(a NewArgs) error {
	policy, _, err := egress.MergeLayers(StartPolicyLayers(a))
	if err != nil {
		return policyError(err)
	}
	if len(policy.Sandbox.SSHKeys) != 0 || a.NamedSSHKeys != nil {
		return namedSSHKeySourceError()
	}
	return nil
}
