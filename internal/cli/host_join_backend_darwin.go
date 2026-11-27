//go:build darwin

package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/helpers"
	"github.com/iamlakshya/simstim/internal/join"
	"github.com/iamlakshya/simstim/internal/join/code"
	"github.com/iamlakshya/simstim/internal/macfacts"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/spf13/cobra"
)

func newPlatformJoinPlanV1(ctx context.Context, invite code.Invite, opts localJoinOptionsV1) (localJoinPlanV1, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// The actual executing root/client binary supplies the checked release.
	// No root stdin field, host probe or mutable inventory chooses these pins.
	data, present := helpers.Manifest()
	if !present || !helpers.Embedded() {
		return nil, &client.UserError{Msg: "this client has no bundled signed Mac host runtime", Hint: "Install a signed client release that includes its pinned Lima and Linux VM image.", Code: "runtime_missing", Kind: client.KindRefused}
	}
	original, err := supply.Load(data)
	if err != nil {
		return nil, client.JoinError(err)
	}
	manifest, err := client.ReleaseManifest()
	if err != nil {
		return nil, err
	}
	// The production accessor must describe the actual same bundled bytes;
	// a development/test override cannot substitute a different release.
	left, _ := json.Marshal(original)
	right, _ := json.Marshal(manifest)
	if string(left) != string(right) {
		return nil, &client.UserError{Msg: "the Mac runtime differs from this executing client's bundled release", Hint: "Install the signed client again before joining this Mac.", Code: "manifest_mismatch", Kind: client.KindRefused}
	}
	resolved, err := join.ResolveDarwinOptions(ctx, join.DarwinOptions{CPUs: opts.CPUs, MemoryBytes: opts.MemoryBytes, AllowACSleepChange: opts.AllowACSleepChange, StartMode: opts.StartMode, AgentUser: opts.AgentUser})
	if err != nil {
		return nil, client.JoinError(err)
	}
	return join.NewDarwinPlan(invite, opts.Account, opts.Addresses, opts.Port, resolved, *original, time.Now())
}

func executePlatformJoinV1(ctx context.Context, invite code.Invite, opts localJoinOptionsV1, out io.Writer) (code.Reply, error) {
	plan, err := newPlatformJoinPlanV1(ctx, invite, opts)
	if err != nil {
		return code.Reply{}, err
	}
	mac, ok := plan.(*join.DarwinPlan)
	if !ok {
		return code.Reply{}, &client.UserError{Msg: "the original Mac join plan changed", Hint: "Run host join again from the Mac's own keyboard.", Code: "join_plan", Kind: client.KindRefused}
	}
	return join.ApplyDarwin(ctx, mac, out)
}

func leavePlatformJoinV1(ctx context.Context, account string, out io.Writer) error {
	return join.LeaveDarwin(ctx, account, out)
}

func localMachineExtraCommandsV1() []*cobra.Command {
	return []*cobra.Command{hostMacFactsCmd(), hostMacVMStartCmd()}
}

func hostMacFactsCmd() *cobra.Command {
	var account string
	command := &cobra.Command{Use: "_mac-facts", Short: "Read fixed Mac host facts through the original joined account", Example: "  " + brand.Name + " host _mac-facts --account dev", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if os.Getenv("SSH_ORIGINAL_COMMAND") != "facts-v1" {
			return &client.UserError{Msg: "this key permits only the fixed facts-v1 action", Hint: "Use the saved Mac host from the laptop.", Code: "mac_facts_action", Kind: client.KindRefused}
		}
		facts, err := macfacts.ReadForAccount(cmd.Context(), account)
		if err != nil {
			return client.JoinError(err)
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(facts)
	}}
	command.Flags().StringVar(&account, "account", "", "the original dedicated Mac account")
	return command
}

func hostMacVMStartCmd() *cobra.Command {
	var account string
	command := &cobra.Command{Use: "_mac-vm-start", Short: "Start the original guarded Mac VM under its native owner", Example: "  " + brand.Name + " host _mac-vm-start --account dev", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return client.JoinError(join.RunDarwinVMGuard(cmd.Context(), account))
	}}
	command.Flags().StringVar(&account, "account", "", "the original dedicated Mac account")
	return command
}
