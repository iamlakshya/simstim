package client

import (
	"context"
	"reflect"

	"github.com/iamlakshya/simstim/internal/api"
)

func checkTrustedJudgeAcknowledgedV1(ctx context.Context, c *Conn, state *api.SessionSecretState) error {
	plan := trustedJudgePlanOfV1(ctx)
	if plan == nil || plan.name == "" {
		return nil
	}
	if plan.conn != c {
		return trustedJudgeSourceErrorV1()
	}
	if plan.family == grantOpResume {
		mode, err := retainedNativeResumeModeLocalV1(ctx, c, plan.id)
		if err != nil {
			return err
		}
		if mode == "warm" {
			return nil
		}
		if mode != "cold" && mode != "rewind" {
			return trustedJudgeSourceErrorV1()
		}
	}
	plan.mu.Lock()
	fetched := plan.fetched && !plan.closed && !plan.busy
	plan.mu.Unlock()
	if !fetched || state == nil || !state.Supported() || state.Generation == "" || !reflect.DeepEqual(state.Intent.TrustedUses, map[string]api.TrustedSecretUseV1{plan.name: plan.ownedUse()}) {
		return trustedJudgeSourceErrorV1()
	}
	return plan.check(ctx)
}
