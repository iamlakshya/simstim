package client

import (
	"context"
	"reflect"

	"github.com/iamlakshya/simstim/internal/api"
)

// This stage captures local metadata only. The native preparation needs
// complete ResumeArgs and therefore precedes all frontend value fetching.
func prepareTrustedJudgeResumeV1(ctx context.Context, c *Conn, id string) error {
	if err := requireGrantOperation(ctx, grantOpResume); err != nil {
		return err
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return err
	}
	creation, err := LoadSandboxPolicy(ctx, c.Host, id)
	if err != nil {
		return err
	}
	if creation == nil || creation.CreationWall == "none" {
		return nil
	}
	if creation.HostID != c.HostID() || creation.FutureDelivery || creation.FutureLineage {
		return trustedJudgeSourceErrorV1()
	}
	r := grantRequestOf(ctx)
	operation, err := r.borrowNewSettings(ctx)
	if err != nil {
		return err
	}
	layers, err := resumeImageLayersV1(ctx, operation, c.Host, creation)
	if err != nil {
		return err
	}
	_, err = captureTrustedJudgeV1(ctx, c, operation, layers, creation.CreationWall, grantOpResume, id, creation, nil)
	return err
}

// The caller runs this before ordinary guest commands as well, so a shared
// name cannot be fetched while its separate judge permission is unanswered.
func checkTrustedJudgeResumeBeforeValuesV1(ctx context.Context, c *Conn, id string) (bool, error) {
	if err := prepareTrustedJudgeResumeV1(ctx, c, id); err != nil {
		return false, err
	}
	plan := trustedJudgePlanOfV1(ctx)
	if plan == nil {
		return false, nil
	}
	if plan.family != grantOpResume || plan.conn != c || plan.id != id {
		return false, trustedJudgeSourceErrorV1()
	}
	mode, err := retainedNativeResumeModeV1(ctx, c, id)
	if err != nil {
		return false, err
	}
	if mode != "warm" && mode != "cold" && mode != "rewind" {
		return false, trustedJudgeSourceErrorV1()
	}
	r := grantRequestOf(ctx)
	r.mu.Lock()
	native := r.namedResume
	r.mu.Unlock()
	plan.mu.Lock()
	if plan.resume != nil && plan.resume != native || plan.cold && mode != "cold" && mode != "rewind" || native == nil {
		plan.mu.Unlock()
		return false, trustedJudgeSourceErrorV1()
	}
	plan.resume, plan.cold = native, mode == "cold" || mode == "rewind"
	plan.mu.Unlock()
	if err := plan.check(ctx); err != nil {
		return false, err
	}
	return mode == "cold" || mode == "rewind", nil
}

func fetchTrustedJudgeResumeV1(ctx context.Context, c *Conn, id string, guestValues map[string]api.Secret, terminal bool) error {
	plan := trustedJudgePlanOfV1(ctx)
	if plan == nil {
		return nil
	}
	if plan.family != grantOpResume || plan.conn != c || plan.id != id {
		return trustedJudgeSourceErrorV1()
	}
	mode, err := retainedNativeResumeModeV1(ctx, c, id)
	if err != nil {
		return err
	}
	plan.mu.Lock()
	cold := plan.cold
	plan.mu.Unlock()
	if mode == "warm" {
		if cold {
			return trustedJudgeSourceErrorV1()
		}
		return nil
	}
	if mode != "cold" && mode != "rewind" || !cold {
		return trustedJudgeSourceErrorV1()
	}
	if err := plan.fetch(ctx, guestValues, terminal); err != nil {
		return err
	}
	mode, err = retainedNativeResumeModeV1(ctx, c, id)
	if err != nil {
		return err
	}
	if mode != "cold" && mode != "rewind" {
		return trustedJudgeSourceErrorV1()
	}
	return plan.check(ctx)
}

func applyTrustedJudgeResumeV1(ctx context.Context, c *Conn, a ResumeArgs) (ResumeArgs, error) {
	plan := trustedJudgePlanOfV1(ctx)
	if plan == nil {
		if len(a.ProtectedTrustedSecretValues) != 0 {
			return a, trustedJudgeSourceErrorV1()
		}
		if a.SecretDelivery != nil && len(a.SecretDelivery.TrustedUses) != 0 {
			return clearInactiveJudgeResumeMetadataV1(ctx, c, a)
		}
		return a, nil
	}
	if plan.family != grantOpResume || plan.conn != c || plan.id != a.ID {
		return a, trustedJudgeSourceErrorV1()
	}
	mode, err := retainedNativeResumeModeLocalV1(ctx, c, a.ID)
	if err != nil {
		return a, err
	}
	if mode == "warm" {
		if len(a.ProtectedTrustedSecretValues) != 0 {
			return a, trustedJudgeSourceErrorV1()
		}
		if a.SecretDelivery != nil {
			owned := api.CloneSecretDeliveryIntent(*a.SecretDelivery)
			owned.TrustedUses = nil
			a.SecretDelivery = &owned
		}
		return a, nil
	}
	plan.mu.Lock()
	cold, native := plan.cold, plan.resume
	plan.mu.Unlock()
	if mode != "cold" && mode != "rewind" || !cold || native == nil {
		return a, trustedJudgeSourceErrorV1()
	}
	native.mu.Lock()
	prepared := native.prepared
	matched := prepared != nil && reflect.DeepEqual(a.Prepared, prepared.Prepared)
	native.mu.Unlock()
	if mode == "rewind" {
		rewind := originalRewindV1(ctx)
		matched = rewind != nil && rewind.named == native && rewind.checkStartMetadataV1(a) == nil
	}
	if !matched {
		return a, trustedJudgeSourceErrorV1()
	}
	if a.SecretDelivery == nil {
		a.SecretDelivery = &api.SecretDeliveryIntent{Version: api.SecretDeliveryIntentV1, Modes: map[string]api.SecretMode{}, Binds: map[string]api.SecretBind{}}
	} else {
		owned := api.CloneSecretDeliveryIntent(*a.SecretDelivery)
		a.SecretDelivery = &owned
	}
	a.SecretDelivery.TrustedUses = nil
	if plan.name != "" {
		a.SecretDelivery.TrustedUses = map[string]api.TrustedSecretUseV1{plan.name: plan.ownedUse()}
	}
	intent, values, err := plan.arguments(ctx, a.SecretDelivery, a.ProtectedTrustedSecretValues)
	if err != nil {
		return a, err
	}
	a.SecretDelivery, a.ProtectedTrustedSecretValues = intent, values
	return a, nil
}

// A disabled judge has no value producer. Only the original retained local
// settings and actual native resume mode may drop its old transport metadata.
// A warm owner keeps its live registry; it receives no role/value update.
func clearInactiveJudgeResumeMetadataV1(ctx context.Context, c *Conn, a ResumeArgs) (ResumeArgs, error) {
	if err := requireGrantOperation(ctx, grantOpResume); err != nil {
		return a, err
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return a, err
	}
	mode, err := retainedNativeResumeModeLocalV1(ctx, c, a.ID)
	if err != nil {
		return a, err
	}
	if mode != "cold" && mode != "warm" && mode != "rewind" {
		return a, trustedJudgeSourceErrorV1()
	}
	r := grantRequestOf(ctx)
	operation, err := r.borrowNewSettings(ctx)
	if err != nil {
		return a, err
	}
	creation, err := getSandboxReceiptPolicyContext(ctx, c, a.ID)
	if err != nil {
		return a, err
	}
	if creation == nil {
		return a, trustedJudgeSourceErrorV1()
	}
	layers, err := resumeImageLayersV1(ctx, operation, c.Host, creation)
	if err != nil {
		return a, err
	}
	policy, err := trustedJudgePolicyV1(layers)
	if err != nil {
		return a, err
	}
	if policy.Classifier.Judge != nil && *policy.Classifier.Judge == "model" {
		return a, trustedJudgeSourceErrorV1()
	}
	owned := api.CloneSecretDeliveryIntent(*a.SecretDelivery)
	owned.TrustedUses = nil
	a.SecretDelivery = &owned
	if err := operation.Revalidate(ctx); err != nil {
		return a, err
	}
	return a, nil
}
