package client

import (
	"context"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

// CheckVMFirstPrepareV1 checks an explicit first construction before values.
// Need must be the SAME client-derived plan bound by H5 to the outgoing request.
// This validation creates no authority from inventory, byte-only ContentRef or
// a host root hash. The host factory independently revalidates actual assets,
// admission and construction; compatibility remains false until actual v2.
func (c *Conn) CheckVMFirstPrepareV1(ctx context.Context, need vmneed.NeedV1, intent string) error {
	if !c.HasFeature(api.FeatureVMNeedV1) {
		return c.grantHelperOld("prepare this client-authorised microvm")
	}
	probe, err := c.CapProbe(ctx, "")
	if err != nil {
		return err
	}
	return checkVMPrepareCandidateV1(need, intent, probe)
}

func checkVMPrepareCandidateV1(need vmneed.NeedV1, intent string, probe api.CapProbe) error {
	if err := need.Validate(probe.Arch); err != nil {
		return &UserError{Msg: "the client VM plan is invalid: " + CleanLine(err.Error()), Hint: "Refresh the client runtime, image pin and agents plan for this architecture.", Code: "vm_expected", Kind: KindRefused}
	}
	if probe.OS != "linux" || intent != "microvm" && intent != "auto" {
		return vmPrepareUnavailable()
	}
	for _, driver := range probe.Walls {
		if driver.Driver == need.Driver && driver.Level == "microvm" {
			if driver.OK || intent == "microvm" && driver.CanPrepare {
				return nil
			}
		}
	}
	return vmPrepareUnavailable()
}
func vmPrepareUnavailable() error {
	return &UserError{Msg: "the host cannot construct this client-authorised microvm now", Hint: "Check host doctor and deliver the required runtime. An explicit --wall microvm may perform the first verified boot when construction prerequisites are available.", Code: "wall_unavailable", Kind: KindFailed}
}
