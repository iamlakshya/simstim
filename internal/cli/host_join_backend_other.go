//go:build !darwin

package cli

import (
	"context"
	"io"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/join"
	"github.com/iamlakshya/simstim/internal/join/code"
)

func newPlatformJoinPlanV1(_ context.Context, invite code.Invite, options localJoinOptionsV1) (localJoinPlanV1, error) {
	if runtime.GOOS != "linux" {
		return nil, &client.UserError{Msg: "machine join needs Linux or a Mac", Hint: "Run host join from the machine's own keyboard.", Code: "join_platform", Kind: client.KindRefused}
	}
	if options.CPUs != 0 || options.MemoryBytes != 0 || options.AllowACSleepChange || options.StartMode != "" || options.AgentUser != "" {
		return nil, &client.UserError{Msg: "Lima and power choices apply only to a Mac join", Hint: "Use host join without the Mac-only preparation flags on Linux.", Code: "join_options", Kind: client.KindUsage}
	}
	return join.NewLinuxPlan(invite, options.Account, options.Addresses, options.Port, time.Now())
}
func executePlatformJoinV1(ctx context.Context, invite code.Invite, options localJoinOptionsV1, out io.Writer) (code.Reply, error) {
	if _, err := newPlatformJoinPlanV1(ctx, invite, options); err != nil {
		return code.Reply{}, err
	}
	plan, err := join.NewLinuxPlan(invite, options.Account, options.Addresses, options.Port, time.Now())
	if err != nil {
		return code.Reply{}, err
	}
	return join.ApplyLinux(ctx, plan, out)
}
func leavePlatformJoinV1(ctx context.Context, account string, out io.Writer) error {
	return join.LeaveLinux(ctx, account, out)
}
func localMachineExtraCommandsV1() []*cobra.Command { return nil }
