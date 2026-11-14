package client

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

// ResumePolicyPlan is a read-only next-start preview. Known means source
// identities and original session values are pinned on this laptop.
type ResumePolicyPlan struct {
	Layers []api.Layer
	Policy api.Policy
	Known  bool
}

// PlanResumePolicy refreshes only locally pinned laptop identities. The host's
// source snapshot supplies trusted-runtime data, never local config authority.
// A legacy session with no pin keeps its current policy and gets no refresh.
func (c *Conn) PlanResumePolicy(ctx context.Context, id string) (ResumePolicyPlan, error) {
	ctx, _ = ensureGrantRequest(ctx)
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return ResumePolicyPlan{}, err
	}
	settings, err := ReadPolicySettings()
	if err != nil {
		return ResumePolicyPlan{}, err
	}
	if !c.HasFeature(PolicyLayerFeature) {
		if !reflect.ValueOf(settings.LayerValues).IsZero() || settings.Lock != nil || len(settings.Repos)+len(settings.Profiles) > 0 || !reflect.ValueOf(c.Host.LayerValues).IsZero() || c.Host.Lock != nil {
			return ResumePolicyPlan{}, c.CheckPolicyLayerFeature([]api.Layer{{Kind: api.LayerAll}})
		}
		return ResumePolicyPlan{}, nil
	}
	policy, err := c.Policy(ctx, id)
	if err != nil {
		var ue *UserError
		if errors.As(err, &ue) && ue.Code == "not_walled" {
			return ResumePolicyPlan{}, nil
		}
		return ResumePolicyPlan{}, err
	}
	local, err := LoadSandboxPolicy(ctx, c.Host, id)
	if err != nil {
		return ResumePolicyPlan{}, err
	}
	if local != nil && local.FutureLineage {
		return ResumePolicyPlan{}, policySourceStateError()
	}
	if local == nil || len(local.PolicySources) == 0 {
		return ResumePolicyPlan{Policy: policy}, nil
	}
	if local.HostID != "" && local.HostID != c.HostID() {
		return ResumePolicyPlan{}, grantIdentityError(c.Host)
	}
	pin, err := api.DecodePolicySourcePin(local.PolicySources)
	if err != nil {
		return ResumePolicyPlan{}, policySourceStateError()
	}
	sources, err := validateSourceSnapshot(policy.Sources, pin)
	if err != nil {
		return ResumePolicyPlan{}, err
	}
	fresh, err := RefreshPolicyLayers(c.Host, sources)
	if err != nil {
		return ResumePolicyPlan{}, err
	}
	merged, _, err := egress.DropOnceRules(fresh, time.Now().UTC())
	if err != nil {
		return ResumePolicyPlan{}, policyError(err)
	}
	var laptop []api.Layer
	for _, layer := range fresh {
		if layer.Source == "" && (layer.Kind == api.LayerAll || layer.Kind == api.LayerHost || layer.Kind == api.LayerRepo || layer.Kind == api.LayerProfile) {
			laptop = append(laptop, layer)
		}
	}
	if err := c.CheckPolicyLayerFeature(laptop); err != nil {
		return ResumePolicyPlan{}, err
	}
	return ResumePolicyPlan{Layers: laptop, Policy: merged, Known: true}, nil
}

// PrepareResumePolicy retains the front-end helper seam for callers that need
// only the named laptop layers. Call PlanResumePolicy before fetching values.
func (c *Conn) PrepareResumePolicy(ctx context.Context, id string) ([]api.Layer, error) {
	plan, err := c.PlanResumePolicy(ctx, id)
	return plan.Layers, err
}

// ResumePlanSecretNames uses only current local settings and pinned session
// values for a known plan. H5 independently authorises each name and exposure.
func ResumePlanSecretNames(h Host, s Session, passed []string, plan ResumePolicyPlan) ([]string, error) {
	if !plan.Known {
		return ResumeSecretNames(h, s, passed)
	}
	names := slices.Clone(plan.Policy.Sandbox.Secrets)
	for _, name := range names {
		if !secretNameRE.MatchString(name) {
			return nil, &UserError{Msg: "the policy contains an invalid secret name", Hint: "Run config check before resuming.", Code: "bad_secret", Kind: KindUsage}
		}
	}
	return SecretNames(Host{}, names), nil
}

// CheckResumePolicyBeforeFetch checks every next-start network allowance
// against H5 before fetching values. It neither writes a grant nor sends layers.
func (c *Conn) CheckResumePolicyBeforeFetch(ctx context.Context, id string, plan ResumePolicyPlan) error {
	tier, _, err := EffectiveSessionTier(ctx, c.Host, id, c.HostID())
	if err != nil {
		return err
	}
	return c.allowLayerGrants(ctx, tier, []api.Layer{{Rules: plan.Policy.Rules}}, false)
}

// CheckResumePolicyGrants checks refreshed network allowances inside the same
// final H5 ownership interval as the session's secret ceiling.
func (c *Conn) CheckResumePolicyGrants(ctx context.Context, tier Tier, layers []api.Layer) error {
	return c.checkLayerGrants(ctx, tier, layers)
}
