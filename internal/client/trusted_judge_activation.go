package client

import (
	"context"
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

// NewSecretArmGrantRequest starts a separate explicit credential operation.
// Attach, exposure and policy replacement answers cannot authorise it.
func NewSecretArmGrantRequest(id string) (*GrantRequest, error) {
	if !api.ValidSessionID(id) {
		return nil, trustedJudgeSourceErrorV1()
	}
	return &GrantRequest{purpose: grantRemote, operation: grantOpSecretArm, id: id}, nil
}

func (c *Conn) trustedJudgeArmViewV1(ctx context.Context, id string, query *api.TrustedSecretReadinessQueryV1) (*api.SandboxCohortView, error) {
	if query != nil && !c.HasFeature(api.FeatureJudgeKeyV1) || !c.HasFeature(api.FeatureBoundSecrets) || !c.HasFeature(api.FeatureSandboxCohort) || !c.HasFeature(api.FeatureSandboxSnapshot) {
		return nil, c.grantHelperOld("arm the original trusted classifier credential")
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return nil, err
	}
	var raw json.RawMessage
	if err := c.Call(ctx, "sandbox.snapshot", api.SandboxSnapshotArgs{ID: id, Cohort: true, TrustedReadiness: api.CloneTrustedSecretReadinessQueryV1(query)}, &raw); err != nil {
		return nil, err
	}
	view, err := api.DecodeSandboxCohort(raw, id)
	if err != nil {
		return nil, trustedJudgeSourceErrorV1()
	}
	if err := egress.ValidateSavedPublicSessionState(view.SecretState); err != nil {
		return nil, err
	}
	if query != nil && (view.TrustedReadiness == nil || !view.TrustedReadiness.ValidFor(*query) || view.SecretState.Generation != query.Generation) || query == nil && view.TrustedReadiness != nil {
		return nil, trustedJudgeSourceErrorV1()
	}
	return view, bindRequestTransportSource(ctx, c)
}

func checkTrustedJudgeResumeHostBeforeValuesV1(ctx context.Context, h Host, id string) (bool, error) {
	plan := trustedJudgePlanOfV1(ctx)
	if plan == nil {
		return false, nil
	}
	if plan.conn == nil || !SameEntry(plan.conn.Host, h) || plan.id != id || plan.family != grantOpResume {
		return false, trustedJudgeSourceErrorV1()
	}
	return checkTrustedJudgeResumeBeforeValuesV1(ctx, plan.conn, id)
}

func fetchTrustedJudgeResumeHostV1(ctx context.Context, h Host, id string, guestValues map[string]api.Secret, terminal bool) error {
	plan := trustedJudgePlanOfV1(ctx)
	if plan == nil {
		return nil
	}
	if plan.conn == nil || !SameEntry(plan.conn.Host, h) || plan.id != id || plan.family != grantOpResume {
		return trustedJudgeSourceErrorV1()
	}
	return fetchTrustedJudgeResumeV1(ctx, plan.conn, id, guestValues, terminal)
}
