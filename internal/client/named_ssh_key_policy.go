package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// The same original request owns one literal local name and action. It holds
// no key value and never changes the immutable policy-source identity pin.
type namedSSHKeyPolicyPlanV1 struct {
	mu        sync.Mutex
	closed    bool
	busy      bool
	pendingID uint64
	wrote     bool
	ack       bool
	conn      *Conn
	stamp     *hostTransportStamp
	operation *settingsOperation
	creation  *SandboxPolicy
	args      api.NamedSSHKeyPolicyArgs
}

func namedSSHPolicySourceError() error {
	return &UserError{Msg: "the SSH key policy change lost its original local source", Hint: "Repeat this policy command from the same verified client connection.", Code: "ssh_key_source", Kind: KindRefused}
}

func prepareNamedSSHKeyPolicyV1(ctx context.Context, c *Conn, id, name string, allow bool) (*namedSSHKeyPolicyPlanV1, error) {
	if err := requireGrantOperation(ctx, grantOpNamedSSHPolicy); err != nil {
		return nil, err
	}
	if api.CheckSecretName(name, false) != api.SecretNameOK {
		return nil, &UserError{Msg: "the SSH key name is invalid", Hint: "Name one SSH key configured in the local secrets table.", Code: "bad_secret", Kind: KindUsage}
	}
	if c == nil || !c.HasFeature(api.FeatureNamedSSHKeysV1) {
		return nil, c.namedSSHPolicyUpgrade()
	}
	op, creation, _, err := retainedNamedSSHPolicySourceV1(ctx, c, id)
	if err != nil {
		return nil, err
	}
	request := grantRequestOf(ctx)
	if request == nil || op == nil || creation == nil {
		return nil, namedSSHPolicySourceError()
	}
	candidate := &namedSSHKeyPolicyPlanV1{conn: c, stamp: c.hostTransport, operation: op, creation: creation, args: api.NamedSSHKeyPolicyArgs{V: 1, Session: id, Name: name, Allow: allow}}
	request.mu.Lock()
	plan := request.namedPolicy
	if plan == nil {
		request.namedPolicy = candidate
		plan = candidate
	}
	request.mu.Unlock()
	if plan.conn != c || plan.stamp != c.hostTransport || plan.operation != op || !sameSandboxCreation(plan.creation, creation) || plan.args != candidate.args {
		return nil, namedSSHPolicySourceError()
	}
	if err := plan.check(ctx, c); err != nil {
		return nil, err
	}
	return plan, nil
}

func (c *Conn) namedSSHPolicyUpgrade() error {
	if c == nil {
		return namedSSHPolicySourceError()
	}
	return c.grantHelperOld("select named SSH keys for the next sandbox start")
}

func (p *namedSSHKeyPolicyPlanV1) check(ctx context.Context, c *Conn) error {
	if err := requireGrantOperation(ctx, grantOpNamedSSHPolicy); err != nil {
		return err
	}
	if p == nil || c == nil || p.conn != c || p.stamp != c.hostTransport {
		return namedSSHPolicySourceError()
	}
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return namedSSHPolicySourceError()
	}
	op, creation, tier, err := retainedNamedSSHPolicySourceV1(ctx, c, p.args.Session)
	if err != nil {
		return err
	}
	if op != p.operation || !sameSandboxCreation(creation, p.creation) {
		return namedSSHPolicySourceError()
	}
	if p.args.Allow {
		settings, err := op.Settings(ctx)
		if err != nil {
			return err
		}
		view, err := NormaliseSettingsSecrets(settings)
		if err != nil {
			return err
		}
		if view.Kinds[p.args.Name] != "ssh-key" || view.Commands[p.args.Name] == "" {
			return &UserError{Msg: "the selected secret is not a configured SSH key", Hint: "Set kind = \"ssh-key\" for this named secret before selecting it.", Code: "ssh_key_kind", Kind: KindUsage}
		}
		if err := allowRequestGrant(ctx, c.Host, c.HostID(), tier, Outbound{Kind: OutboundSSHKey, Name: p.args.Name}, settings); err != nil {
			return err
		}
	}
	return op.Revalidate(ctx)
}

// beforeRequest is called by H5 at the actual queued physical write turn.
// Neither a borrowed generic request nor changed wire arguments may publish.
func (p *namedSSHKeyPolicyPlanV1) beforeRequest(ctx context.Context, c *Conn, request proto.Request) error {
	if p == nil || request.Op != "ssh-key.policy" {
		return namedSSHPolicySourceError()
	}
	var args api.NamedSSHKeyPolicyArgs
	fields, err := api.JSONObjectFields(request.Args, "v", "session", "name", "allow")
	if err != nil || len(fields) != 4 || !api.ExactJSONFields(request.Args, args) || json.Unmarshal(request.Args, &args) != nil || args != p.args {
		return namedSSHPolicySourceError()
	}
	if err := p.check(ctx, c); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.busy || p.closed || p.pendingID != 0 {
		return namedSSHPolicySourceError()
	}
	p.pendingID = request.ID
	return nil
}

// CheckNamedSSHKeyPolicy inspects only the local source and selected name.
// It never runs a secret command or changes a running signer.
func (c *Conn) CheckNamedSSHKeyPolicy(ctx context.Context, id, name string, allow bool) error {
	_, err := prepareNamedSSHKeyPolicyV1(ctx, c, id, name, allow)
	return err
}

// CheckNamedSSHKeyGrant retains the positive inspect-only command seam.
func (c *Conn) CheckNamedSSHKeyGrant(ctx context.Context, id, name string) error {
	return c.CheckNamedSSHKeyPolicy(ctx, id, name, true)
}

// ChangeNamedSSHKeyPolicy changes names for the next actual sandbox start.
// Only the explicit local action is recorded after a matching protected ACK.
// Other names returned by a host never enter the local eligible key set.
func (c *Conn) ChangeNamedSSHKeyPolicy(ctx context.Context, id, name string, allow bool) (api.NamedSSHKeyPolicyResult, error) {
	plan, err := prepareNamedSSHKeyPolicyV1(ctx, c, id, name, allow)
	if err != nil {
		return api.NamedSSHKeyPolicyResult{}, err
	}
	plan.mu.Lock()
	if plan.closed || plan.busy {
		plan.mu.Unlock()
		return api.NamedSSHKeyPolicyResult{}, namedSSHPolicySourceError()
	}
	plan.busy = true
	plan.mu.Unlock()
	defer func() { plan.mu.Lock(); plan.busy = false; plan.mu.Unlock() }()
	ctx, release, err := c.beginGrant(ctx, "SSH key policy", false)
	if err != nil {
		return api.NamedSSHKeyPolicyResult{}, err
	}
	defer release()
	if err := plan.check(ctx, c); err != nil {
		return api.NamedSSHKeyPolicyResult{}, err
	}
	var raw json.RawMessage
	if err := c.callWithSent(ctx, "ssh-key.policy", plan.args, &raw, release); err != nil {
		return api.NamedSSHKeyPolicyResult{}, err
	}
	result, err := plan.acknowledged(raw)
	if err != nil {
		return api.NamedSSHKeyPolicyResult{}, err
	}
	if err := plan.check(ctx, c); err != nil {
		return api.NamedSSHKeyPolicyResult{}, err
	}
	if err := recordNamedSSHKeyChoiceV1(ctx, c, plan.creation, plan.args.Name, plan.args.Allow); err != nil {
		return api.NamedSSHKeyPolicyResult{}, err
	}
	return result, nil
}

func (p *namedSSHKeyPolicyPlanV1) acknowledged(raw []byte) (api.NamedSSHKeyPolicyResult, error) {
	if p == nil {
		return api.NamedSSHKeyPolicyResult{}, namedSSHPolicySourceError()
	}
	var result api.NamedSSHKeyPolicyResult
	fields, err := api.JSONObjectFields(raw, "v", "names", "applies")
	view := api.NamedSSHKeyViewV1{V: 1}
	if err != nil || len(fields) != 3 || !api.ExactJSONFields(raw, result) || json.Unmarshal(raw, &result) != nil || result.V != 1 || result.Applies != "next_start" {
		return result, namedSSHPolicySourceError()
	}
	view.NextStart = result.Names
	if !view.Valid() || slices.Contains(result.Names, p.args.Name) != p.args.Allow {
		return result, namedSSHPolicySourceError()
	}
	result.Names = slices.Clone(result.Names)
	return result, nil
}

func (p *namedSSHKeyPolicyPlanV1) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }

func (p *namedSSHKeyPolicyPlanV1) close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.busy {
		return namedSSHPolicySourceError()
	}
	p.closed = true
	return nil
}

// wroteRequest is called by the actual ordered writer after this exact frame.
func (p *namedSSHKeyPolicyPlanV1) wroteRequest(c *Conn, id uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.busy && !p.closed && p.conn == c && p.pendingID == id && id != 0 {
		p.wrote = true
	}
}

// afterResponse is called by the same original pending call, before decoding
// its user result. A caller-supplied ACK cannot reach this private seam.
func (p *namedSSHKeyPolicyPlanV1) afterResponse(ctx context.Context, c *Conn, op string, id uint64, resp proto.Response) error {
	if p == nil || op != "ssh-key.policy" || p.conn != c || id == 0 || resp.ID != id {
		return namedSSHPolicySourceError()
	}
	p.mu.Lock()
	valid := p.busy && !p.closed && p.wrote && p.pendingID == id && !p.ack
	p.mu.Unlock()
	if !valid || !resp.OK {
		return namedSSHPolicySourceError()
	}
	if _, err := p.acknowledged(resp.Result); err != nil {
		return err
	}
	if err := p.check(ctx, c); err != nil {
		return err
	}
	p.mu.Lock()
	p.ack = true
	p.mu.Unlock()
	return nil
}

func (p *namedSSHKeyPolicyPlanV1) checkAcknowledgedLocalChoiceV1(ctx context.Context, c *Conn, creation *SandboxPolicy, name string, allow bool) error {
	if p == nil || p.conn != c || !sameSandboxCreation(p.creation, creation) || p.args.Name != name || p.args.Allow != allow {
		return namedSSHPolicySourceError()
	}
	r := grantRequestOf(ctx)
	if r == nil {
		return namedSSHPolicySourceError()
	}
	r.mu.Lock()
	same := r.namedPolicy == p
	r.mu.Unlock()
	p.mu.Lock()
	valid := p.busy && !p.closed && p.wrote && p.ack && p.pendingID != 0
	p.mu.Unlock()
	if !same || !valid {
		return namedSSHPolicySourceError()
	}
	return p.check(ctx, c)
}
