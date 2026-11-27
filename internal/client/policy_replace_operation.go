package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"slices"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

// PolicyReplacement retains its actual accepted connection and local sources.
// Its public preview cannot construct another operation or approve a send.
type PolicyReplacement struct{ operation *policyReplaceOperation }

// PolicyReplacementPreview describes source differences without granting them.
type PolicyReplacementPreview struct {
	Session   string         `json:"session"`
	Changed   bool           `json:"changed"`
	Layers    []api.LayerRef `json:"layers,omitempty"`
	NextStart []string       `json:"next_start,omitempty"`
}

type policyReplaceOperation struct {
	mu                      sync.Mutex
	request                 *GrantRequest
	session                 string
	conn                    *Conn
	stamp                   *hostTransportStamp
	settings                *settingsOperation
	creation                *SandboxPolicy
	tier                    Tier
	currentHash             string
	layers                  []api.Layer
	policy                  api.Policy
	preview                 PolicyReplacementPreview
	wire                    *api.PolicyReplaceArgsV1
	result                  *api.PolicyReplaceResultV1
	busy, closed, uncertain bool
	attempts                int
}

func (*policyReplaceOperation) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }

// NewPolicyReplaceGrantRequest fixes the session before connection or questions.
func NewPolicyReplaceGrantRequest(session string) *GrantRequest {
	r := &GrantRequest{purpose: grantRemote, operation: grantOpPolicyReplace}
	r.policyReplace = &policyReplaceOperation{request: r, session: session}
	return r
}

func requirePolicyReplaceContext(ctx context.Context) (*policyReplaceOperation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r := grantRequestOf(ctx)
	if r == nil {
		return nil, policyReplaceSourceError()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.lifetimeErrorLocked(); err != nil {
		return nil, err
	}
	if r.purpose != grantRemote || r.operation != grantOpPolicyReplace || r.SSHAgent || r.Untrusted || r.policyReplace == nil {
		return nil, policyReplaceSourceError()
	}
	return r.policyReplace, nil
}

func policyReplaceSourceError() error {
	return &UserError{Msg: "the policy replacement lost its original session or local source", Hint: "Read the policy again and start a new apply request on its original host.", Code: "policy_sources", Kind: KindRefused}
}

// PlanPolicyReplacement reads the applied snapshot but selects settings only
// from this laptop's original creation pin. It fetches no secret or key value.
func (c *Conn) PlanPolicyReplacement(ctx context.Context, session string) (*PolicyReplacement, error) {
	o, err := requirePolicyReplaceContext(ctx)
	if err != nil {
		return nil, err
	}
	if o.session != session || !api.ValidSessionID(session) {
		return nil, policyReplaceSourceError()
	}
	if err := checkAcceptedHostTransport(c); err != nil {
		return nil, err
	}
	if c.agentOnly || c.hostTransport.kind != hostTransportSaved || !ValidHostID(c.HostID()) || c.HostIDChanged() {
		return nil, policyReplaceSourceError()
	}
	if !c.HasFeature(api.FeaturePolicyLiveV1) {
		return nil, c.grantHelperOld("apply selected policy layers to a running sandbox")
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return nil, err
	}
	o.mu.Lock()
	if o.closed || o.busy || o.conn != nil {
		o.mu.Unlock()
		return nil, policyReplaceSourceError()
	}
	o.busy = true
	o.mu.Unlock()
	defer func() { o.mu.Lock(); o.busy = false; o.mu.Unlock() }()
	settings, err := o.request.borrowNewSettings(ctx)
	if err != nil {
		return nil, err
	}
	tier, creation, err := EffectiveSessionTier(ctx, c.Host, session, c.HostID())
	if err != nil {
		return nil, err
	}
	if !tier.Valid() || creation == nil || len(creation.PolicySources) == 0 || creation.HostID != c.HostID() {
		return nil, policyReplaceSourceError()
	}
	if creation.FutureLineage || creation.FutureDelivery || creation.futureDeliveryReceipt || creation.futureInputOperation {
		return nil, policyReplaceSourceError()
	}
	if err := c.needPolicy(session); err != nil {
		return nil, err
	}
	var rawPolicy json.RawMessage
	if err := c.Call(ctx, "policy.ls", map[string]string{"id": session}, &rawPolicy); err != nil {
		return nil, err
	}
	var policy api.Policy
	if !api.ExactJSONFields(rawPolicy, policy) || json.Unmarshal(rawPolicy, &policy) != nil {
		return nil, policyReplaceSourceError()
	}
	pin, err := api.DecodePolicySourcePin(creation.PolicySources)
	if err != nil {
		return nil, policyReplaceSourceError()
	}
	sources, err := validateSourceSnapshot(policy.Sources, pin)
	if err != nil {
		return nil, err
	}
	current := api.CloneLayers(sources)
	if len(policy.NextLayers) != 0 {
		current, err = api.ReplacePolicyLaptopLayersV1(current, policy.NextLayers)
		if err != nil {
			return nil, policyReplaceSourceError()
		}
	}
	hash, err := api.HashPolicyLaptopLayersV1(current)
	if err != nil || hash != policy.LayersHash || !api.ValidPolicyHashV1(hash) {
		return nil, policyReplaceSourceError()
	}
	values, err := settings.Settings(ctx)
	if err != nil {
		return nil, err
	}
	fresh, err := refreshPolicyLayers(values, c.Host, sources)
	if err != nil {
		return nil, err
	}
	merged, _, err := egress.MergeLayers(fresh)
	if err != nil {
		return nil, err
	}
	laptop, err := api.PolicyLaptopLayersV1(fresh)
	if err != nil || len(laptop) == 0 {
		return nil, policyReplaceSourceError()
	}
	if err := c.CheckPolicyLayerFeature(laptop); err != nil {
		return nil, err
	}
	if err := c.CheckHTTPMethodFeature(laptop); err != nil {
		return nil, err
	}
	want, err := api.HashPolicyLaptopLayersV1(laptop)
	if err != nil {
		return nil, err
	}
	_, nextStart, err := egress.ProjectLivePolicyLayersV1(sources, fresh)
	if err != nil {
		return nil, err
	}
	previous, _ := api.PolicyLaptopLayersV1(current)
	preview := PolicyReplacementPreview{Session: session, Changed: hash != want, NextStart: nextStart}
	for at, layer := range laptop {
		if at >= len(previous) || !reflect.DeepEqual(previous[at], layer) {
			preview.Layers = append(preview.Layers, api.LayerRef{Kind: layer.Kind, Name: layer.Name, Source: layer.Source})
		}
	}
	o.mu.Lock()
	o.conn, o.stamp, o.settings, o.creation, o.tier = c, c.hostTransport, settings, cloneSandboxPolicy(creation), min(tier, creation.Ceiling)
	o.currentHash, o.layers, o.preview, o.policy = hash, api.CloneLayers(laptop), preview, api.ClonePolicy(merged)
	o.mu.Unlock()
	if err := o.checkLocal(ctx); err != nil {
		return nil, err
	}
	return &PolicyReplacement{operation: o}, nil
}

func (o *policyReplaceOperation) checkLocal(ctx context.Context) error {
	current, err := requirePolicyReplaceContext(ctx)
	if err != nil {
		return err
	}
	if o == nil || current != o {
		return policyReplaceSourceError()
	}
	o.mu.Lock()
	closed, conn, stamp, settings, original := o.closed, o.conn, o.stamp, o.settings, o.creation
	o.mu.Unlock()
	if conn == nil || stamp == nil || stamp != conn.hostTransport || settings == nil {
		return policyReplaceSourceError()
	}
	if closed {
		return policyReplaceSourceError()
	}
	if err := conn.CheckSavedSource(ctx); err != nil {
		return err
	}
	if err := settings.Revalidate(ctx); err != nil {
		return err
	}
	tier, creation, err := EffectiveSessionTier(ctx, conn.Host, o.session, conn.HostID())
	if err != nil {
		return err
	}
	if !tier.Valid() || !sameSandboxCreation(creation, original) || creation == nil || creation.FutureLineage || creation.FutureDelivery || creation.futureDeliveryReceipt || creation.futureInputOperation {
		return policyReplaceSourceError()
	}
	o.mu.Lock()
	o.tier = min(o.tier, tier, creation.Ceiling, requestCeiling(ctx, tier))
	o.mu.Unlock()
	return nil
}

// Context keeps questions bound to the same private request as the plan.
func (p *PolicyReplacement) Context(ctx context.Context) context.Context {
	if p == nil || p.operation == nil {
		return ctx
	}
	return WithGrantRequest(ctx, p.operation.request)
}

func (p *PolicyReplacement) Preview() PolicyReplacementPreview {
	if p == nil || p.operation == nil {
		return PolicyReplacementPreview{}
	}
	p.operation.mu.Lock()
	defer p.operation.mu.Unlock()
	v := p.operation.preview
	v.Layers = slices.Clone(v.Layers)
	v.NextStart = slices.Clone(v.NextStart)
	return v
}

// Check checks the original desired allowances before parallel publication.
// Apply checks them again inside the physical Source publication interval.
func (p *PolicyReplacement) Check(ctx context.Context) error {
	if p == nil || p.operation == nil {
		return policyReplaceSourceError()
	}
	o := p.operation
	if err := o.checkLocal(ctx); err != nil {
		return err
	}
	return o.checkGrants(ctx)
}

func (o *policyReplaceOperation) checkGrants(ctx context.Context) error {
	if err := o.checkLocal(ctx); err != nil {
		return err
	}
	o.mu.Lock()
	tier, policy := o.tier, api.ClonePolicy(o.policy)
	o.mu.Unlock()
	layers := []api.Layer{{Rules: policy.Rules}}
	if err := o.conn.checkPublicOnlyFeature(layers); err != nil {
		return err
	}
	settings, err := o.settings.Settings(ctx)
	if err != nil {
		return err
	}
	for _, rule := range policy.Rules {
		if !rule.Allow || rule.Door != "" && rule.Door != api.DoorNet {
			continue
		}
		outbound, err := policyOutboundForRule(rule)
		if err != nil {
			return err
		}
		if err := allowRequestGrant(ctx, o.conn.Host, o.conn.HostID(), tier, outbound, settings); err != nil {
			return err
		}
	}
	return nil
}

func (p *PolicyReplacement) Close() error {
	if p == nil || p.operation == nil {
		return nil
	}
	return p.operation.request.Close()
}
func (o *policyReplaceOperation) close() error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.busy {
		return policyReplaceSourceError()
	}
	o.closed = true
	o.layers = nil
	o.wire = nil
	o.creation = nil
	return nil
}

func decodePolicyReplaceResult(raw json.RawMessage) (api.PolicyReplaceResultV1, error) {
	var result api.PolicyReplaceResultV1
	if !api.ExactJSONFields(raw, result) || json.Unmarshal(raw, &result) != nil || result.Validate() != nil {
		return result, &UserError{Msg: "the host returned an invalid policy replacement acknowledgement", Hint: "Read the current policy with matching helpers before applying again.", Code: "policy_protocol", Kind: KindRefused}
	}
	return result, nil
}
