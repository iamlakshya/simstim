package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/onboard"
	"github.com/iamlakshya/simstim/internal/provider"
)

type sessionMachineProviderV1 interface {
	CreateMachineV1(context.Context, provider.Request) (client.Host, client.SessionMachineResourceV1, error)
}

func checkNewMachineFlagsV1(cmd *cobra.Command, args []string, name string) error {
	if name == "" {
		return nil
	}
	if name != "e2b" {
		return &client.UserError{Msg: fmt.Sprintf("%s cannot make a machine for each session. Only e2b can.", client.CleanLine(name)), Hint: brand.Name + " new --machine e2b", Code: "machine_provider", Kind: client.KindUsage}
	}
	if selectedNewArgumentV1(cmd, args) != "" || cmd.Flags().Changed("in") || cmd.Flags().Changed("need") || cmd.Flags().Changed("dry-run") || cmd.Flags().Changed("allow-wake") {
		return policyUsage("--machine makes its own host and cannot select another host or sandbox", "Use new --machine e2b without a host, --in or automatic placement flags.", "flags_conflict")
	}
	return nil
}

func prepareNewMachineV1(cmd *cobra.Command, name string, args client.NewArgs) (owner *client.SessionMachineOwnerV1, updated client.NewArgs, returned error) {
	updated = args
	sessionName, err := client.SessionMachineNameV1(args.Name)
	if err != nil {
		return nil, args, err
	}
	settings, err := client.ReadPolicySettings()
	if err != nil {
		return nil, args, err
	}
	p, err := findProvider(name)
	if err != nil {
		return nil, args, err
	}
	producer, ok := p.(sessionMachineProviderV1)
	if !ok {
		return nil, args, &client.UserError{Msg: "this provider has no owned machine creation path", Hint: "Install a build with E2B machine creation.", Code: "machine_provider", Kind: client.KindFailed}
	}
	req := provider.Request{Name: "m-" + sessionName, Template: settings.Machines.E2B.Template, TokenCommand: settings.Machines.E2B.TokenCommand, Out: prompts(cmd).out}
	owner, err = client.BeginSessionMachineV1(cmd.Context(), onboard.CreationRequest(p, req), sessionName, args.Downgrade, func(ctx context.Context) (client.Host, client.SessionMachineResourceV1, error) {
		return producer.CreateMachineV1(ctx, req)
	})
	if err != nil {
		var userErr *client.UserError
		if errors.As(err, &userErr) && userErr.Code == "no_api_key" {
			err = &client.UserError{Msg: "No API key for e2b: E2B_API_KEY is not set, and the E2B CLI is not logged in.", Hint: "Add [machines.e2b] token_command = \"<command>\" to config.toml", Code: "machine_token", Kind: client.KindFailed}
		}
		return owner, args, err
	}
	registration := owner.RegistrationContext()
	prompter := &prompter{ctx: registration, out: prompts(cmd).out}
	if err := addHost(registration, prompter, owner.Host(), true); err != nil {
		return owner, args, err
	}
	if err := owner.FinishRegistration(); err != nil {
		return owner, args, err
	}
	ctx := owner.Context(cmd.Context())
	target := owner.Target()
	probe, _, err := client.ProbeHostPrepareV1(ctx, target, "", false, false)
	if err != nil {
		return owner, args, err
	}
	printRootPrepareProbe(prompts(cmd).out, target.Name(), probe)
	if _, err := client.DeliverHostPrepareRuntimeV1(ctx, target, "", false, prompts(cmd).out, nil); err != nil {
		return owner, args, err
	}
	if _, err := client.RunHostPrepareRootPhaseV1(ctx, target, client.HostPrepareRootOptionsV1{
		ExplicitMode: client.RootRun, Yes: true, Out: prompts(cmd).out,
		ShowScript: func(script client.RootScript) { showRootScript(prompts(cmd).out, target.Name(), script) },
	}); err != nil {
		return owner, args, err
	}
	if _, _, err := client.BootAndRecordHostPrepareV1(ctx, target, "", client.HostPrepareOptionsV1{Progress: prompts(cmd).out}, false, false, nil); err != nil {
		return owner, args, err
	}
	updated.Name = sessionName
	return owner, updated, nil
}
