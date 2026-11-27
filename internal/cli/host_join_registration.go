package cli

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

type joinedAcceptOptionsV1 struct {
	sshConfig string
	prepare   hostPrepareFlowOpts
	noSandbox bool
}
type joinedAcceptFlagsV1 struct {
	rootMode, images                        string
	delegateIO, separateAccounts, noSandbox bool
}

func addInviteAcceptFlagsV1(cmd *cobra.Command, flags *joinedAcceptFlagsV1) {
	if cmd.Flags().Lookup("root-step") == nil {
		addRootStep(cmd, &flags.rootMode, "with --accept, handle optional root changes: run, print or skip")
	}
	if cmd.Flags().Lookup("images") == nil {
		addImages(cmd, &flags.images, "with --accept, get runtime and image bytes from auto, host or laptop")
	}
	if cmd.Flags().Lookup("delegate-io") == nil {
		cmd.Flags().BoolVar(&flags.delegateIO, "delegate-io", false, "with --accept, offer disk I/O controller delegation")
	}
	if cmd.Flags().Lookup("separate-accounts") == nil {
		cmd.Flags().BoolVar(&flags.separateAccounts, "separate-accounts", false, "with --accept, offer the signed launcher and separate sandbox accounts")
	}
	if cmd.Flags().Lookup("no-sandbox") == nil {
		cmd.Flags().BoolVar(&flags.noSandbox, "no-sandbox", false, "with --accept, save registration without sandbox preparation")
	}
}
func parseInviteAcceptOptionsV1(cmd *cobra.Command, yes bool) (joinedAcceptOptionsV1, error) {
	rootMode, _ := cmd.Flags().GetString("root-step")
	images, _ := cmd.Flags().GetString("images")
	delegate, _ := cmd.Flags().GetBool("delegate-io")
	accounts, _ := cmd.Flags().GetBool("separate-accounts")
	noSandbox, _ := cmd.Flags().GetBool("no-sandbox")
	options, err := parseHostPrepareFlags(cmd, rootMode, images, delegate, accounts, yes, false)
	if err != nil {
		return joinedAcceptOptionsV1{}, err
	}
	if noSandbox && (cmd.Flags().Changed("root-step") || cmd.Flags().Changed("images") || delegate || accounts) {
		return joinedAcceptOptionsV1{}, &client.UserError{Msg: "--no-sandbox cannot also request sandbox preparation", Hint: "Remove --no-sandbox, or omit the preparation flags.", Code: "bad_value", Kind: client.KindUsage}
	}
	return joinedAcceptOptionsV1{prepare: options, noSandbox: noSandbox}, nil
}

func registerAndPrepareJoinedHostV1(cmd *cobra.Command, p *prompter, lease *client.InviteAcceptance, h client.Host, keys []string, tier client.Tier, yes bool, options joinedAcceptOptionsV1) error {
	registration, err := lease.RegistrationContextV1()
	if err != nil {
		return err
	}
	pin := hostKeyOptions(h, "", false, p.on)
	pin.Wants = append([]string(nil), keys...)
	checks, registrationErr := connectHost(registration, p.out, client.HostChange{Kind: "host add --accept", Was: nil, Now: &h, Pin: true, Tier: tier}, nil, installConsent(cmd.Context(), p, h, yes), p.changes(), pin)
	target, targetErr := lease.RegistrationTargetV1()
	if target != nil {
		if err = client.RecordInviteRegistrationV1(lease.Context(), target); err != nil {
			return errors.Join(registrationErr, err)
		}
	}
	if registrationErr != nil {
		if isCancel(registrationErr) || cmd.Context().Err() != nil {
			return p.cancelOf(registrationErr)
		}
		return registrationErr
	}
	if targetErr != nil {
		return targetErr
	}
	// Discard registration's capture/observation/OP context. Every H10 phase
	// creates its own existing exact request and grant from this clean caller.
	var summary hostPrepareSummary
	if !options.noSandbox {
		if summary, err = runHostPrepareTargetV1(cmd, p, cmd.Context(), target, options.prepare); err != nil {
			if isCancel(err) || cmd.Context().Err() != nil {
				return p.cancelOf(err)
			}
			return err
		}
	}
	if err = offerAddedHostAgents(cmd.Context(), p, target.Host(), yes, checks); err != nil {
		return err
	}
	printJoinedHostOutcomeV1(p.out, h.Name, options.noSandbox, summary)
	return nil
}
func printJoinedHostOutcomeV1(out io.Writer, name string, noSandbox bool, summary hostPrepareSummary) {
	if !noSandbox && summary.tested() {
		fmt.Fprintf(out, "\n%s is ready. Start a session with `%s new %s --agent claude`, or run `%s`.\n", name, brand.Name, name, brand.Name)
		return
	}
	if noSandbox {
		fmt.Fprintf(out, "\n%s was saved. Sandbox preparation was skipped.\n", name)
	} else if summary.outcome.noRunnableWall {
		fmt.Fprintf(out, "\n%s was saved, but no runnable sandbox wall was confirmed.\n", name)
	} else {
		fmt.Fprintf(out, "\n%s was saved. Its sandbox test did not confirm readiness.\n", name)
	}
	fmt.Fprintf(out, "Run %s host prepare %s to prepare and test its sandbox.\n", brand.Name, shellWord(name))
}
