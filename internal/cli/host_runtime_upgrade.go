package cli

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

var deliverHostPrepareRuntime = client.DeliverHostPrepareRuntimeV1

// prepareHostRuntimeTargetV1 runs Phase 2 of host sandbox preparation on the
// already-opened saved preparation target. Its runtime-upgrade purpose and
// owned connection close fully before any root-step or test-boot request.
func prepareHostRuntimeTargetV1(cmd *cobra.Command, p *prompter, baseCtx context.Context, target *client.HostPrepareTargetV1, explicitImages string, wake bool) (api.RuntimeStatus, error) {
	return deliverHostPrepareRuntime(baseCtx, target, explicitImages, wake, p.out, func(gctx context.Context, run func(context.Context) error) (context.Context, error) {
		return grantQuestionsOn(cmd, p, gctx, run)
	})
}
