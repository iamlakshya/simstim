package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func policyApplyCmd() *cobra.Command {
	var dryRun, yes, asJSON bool
	cmd := &cobra.Command{Use: "apply", Short: "Preview and apply current policy files to running sandboxes", Args: cobra.NoArgs,
		Long:    "Shows each running sandbox whose selected laptop layers changed, then asks before applying. Network and classifier settings, the preset and push rules apply at once. Other settings apply at the next start. Hosts are contacted in parallel with a five-second limit each. Unreached hosts stay pending for their next contact.",
		Example: "  " + brand.Name + " policy apply --dry-run\n  " + brand.Name + " policy apply -y", RunE: func(cmd *cobra.Command, _ []string) error { return runPolicyApply(cmd, nil, dryRun, yes, asJSON, true) }}
	addDryRun(cmd, &dryRun, "show each sandbox's changes without sending replacements")
	addYes(cmd, &yes, "apply the displayed changes")
	addJSON(cmd, &asJSON)
	return cmd
}

func runPolicyApply(cmd *cobra.Command, layer *client.FileLayer, dryRun, yes, asJSON, ask bool) error {
	if ask && !dryRun && !yes && !inputOn(cmd) {
		return &client.UserError{Msg: "policy apply needs an answer before changing running sandboxes", Hint: prompts(cmd).withFlags("-y"), Code: "needs_answer", Kind: client.KindNeedsAnswer}
	}
	if _, err := client.ReadPolicySettings(); err != nil {
		return err
	}
	if !dryRun {
		change, err := client.BeginPolicyChange(cmd.Context())
		if err != nil {
			return err
		}
		defer change.Close()
		original := cmd.Context()
		cmd.SetContext(change.Context(original))
		defer cmd.SetContext(original)
	}
	return runPolicyApplyLocked(cmd, layer, dryRun, yes, asJSON, ask)
}

func runPolicyApplyLocked(cmd *cobra.Command, layer *client.FileLayer, dryRun, yes, asJSON, ask bool) error {
	plan, err := client.PlanPolicyPropagation(cmd.Context(), layer)
	if err != nil {
		return err
	}
	defer plan.Close()
	applications := plan.Applications()
	previews := []map[string]any{}
	for _, application := range applications {
		preview := application.Replacement.Preview()
		if !preview.Changed {
			continue
		}
		previews = append(previews, map[string]any{"host": application.Host, "change": preview})
		if !asJSON {
			say(cmd, "%s/%s: selected policy layers changed", client.CleanLine(application.Host), client.CleanLine(preview.Session))
			if len(preview.NextStart) > 0 {
				say(cmd, "; at the next start: %s", strings.Join(preview.NextStart, ", "))
			}
			say(cmd, ".\n")
		}
	}
	if dryRun {
		if asJSON {
			return printJSON(map[string]any{"dry_run": true, "changes": previews, "unreached": plan.Unreached()})
		}
		printPolicyUnreached(cmd, plan.Unreached())
		return nil
	}
	if ask && !yes && len(previews) > 0 {
		accepted, err := prompts(cmd).yesNo("Apply these policy changes to the listed sandboxes?", false)
		if err != nil {
			return err
		}
		if !accepted {
			return prompts(cmd).cancel()
		}
	}
	original := cmd.Context()
	defer cmd.SetContext(original)
	for _, application := range applications {
		if !application.Replacement.Preview().Changed {
			continue
		}
		cmd.SetContext(application.Replacement.Context(original))
		if err := grantQuestions(cmd, func(ctx context.Context) error { return application.Replacement.Check(ctx) }); err != nil {
			plan.Block(application.Host, err)
		}
	}
	cmd.SetContext(original)
	result, err := plan.Apply(original)
	if err != nil {
		return err
	}
	if asJSON {
		return printJSON(result)
	}
	for _, change := range result.Sessions {
		if change.Hash == "" {
			continue
		}
		say(cmd, "%s/%s: policy %s", client.CleanLine(change.Host), client.CleanLine(change.Session), change.State)
		if len(change.NextStart) > 0 {
			say(cmd, "; at the next start: %s", strings.Join(change.NextStart, ", "))
		}
		say(cmd, ".\n")
	}
	printPolicyUnreached(cmd, result.Unreached)
	return nil
}

func printPolicyUnreached(cmd *cobra.Command, hosts []string) {
	if len(hosts) > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "Policy pending on: %s. It will be retried at the next contact.\n", client.CleanLine(strings.Join(hosts, ", ")))
	}
}

func propagateFilePolicy(cmd *cobra.Command, layer client.FileLayer) (client.PolicyPropagationResult, error) {
	plan, err := client.PlanPolicyPropagation(cmd.Context(), &layer)
	if err != nil {
		return client.PolicyPropagationResult{}, err
	}
	defer plan.Close()
	original := cmd.Context()
	defer cmd.SetContext(original)
	for _, application := range plan.Applications() {
		if !application.Replacement.Preview().Changed {
			continue
		}
		cmd.SetContext(application.Replacement.Context(original))
		if err := grantQuestions(cmd, func(ctx context.Context) error { return application.Replacement.Check(ctx) }); err != nil {
			plan.Block(application.Host, err)
		}
	}
	cmd.SetContext(original)
	return plan.Apply(original)
}

func beginPolicyFileCommand(cmd *cobra.Command, dryRun bool) (func(), error) {
	if dryRun {
		return func() {}, nil
	}
	change, err := client.BeginPolicyChange(cmd.Context())
	if err != nil {
		return nil, err
	}
	original := cmd.Context()
	cmd.SetContext(change.Context(original))
	return func() { cmd.SetContext(original); _ = change.Close() }, nil
}

func printFilePolicyPropagation(cmd *cobra.Command, result client.PolicyPropagationResult) {
	for _, change := range result.Sessions {
		if change.Hash != "" {
			say(cmd, "%s/%s: policy %s", client.CleanLine(change.Host), client.CleanLine(change.Session), change.State)
			if len(change.NextStart) > 0 {
				say(cmd, "; at the next start: %s", strings.Join(change.NextStart, ", "))
			}
			say(cmd, ".\n")
		}
	}
	printPolicyUnreached(cmd, result.Unreached)
}

func noPolicyReplayForDryRun(cmd *cobra.Command, dryRun bool) func() {
	if !dryRun {
		return func() {}
	}
	original := cmd.Context()
	cmd.SetContext(client.WithoutPolicyReplay(original))
	return func() { cmd.SetContext(original) }
}
