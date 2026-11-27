package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func policyLearnCmd() *cobra.Command {
	var layer policyLayerFlags
	var dryRun, yes, asJSON bool
	cmd := &cobra.Command{Use: "learn <host>/<session>", Short: "Turn selected observed network destinations into policy rules", Args: cobra.ExactArgs(1), ValidArgsFunction: firstArg(completeSessions),
		Example: "  " + brand.Name + " policy learn my-vps/auth-fix --repo . --dry-run\n  " + brand.Name + " policy learn my-vps/auth-fix --profile review -y", RunE: func(cmd *cobra.Command, args []string) error {
			restoreReplay := noPolicyReplayForDryRun(cmd, dryRun)
			defer restoreReplay()
			if layer.count() != 1 {
				return policyUsage("policy learn needs one file layer", "Pass --all, --repo or --profile.", "layer_missing")
			}
			if !dryRun && !yes && !inputOn(cmd) {
				return &client.UserError{Msg: "policy learn needs an answer before writing observed destinations", Hint: prompts(cmd).withFlags("-y"), Code: "needs_answer", Kind: client.KindNeedsAnswer}
			}
			selected, err := layer.file(cmd.Context())
			if err != nil {
				return err
			}
			return withSession(cmd, args[0], func(ctx context.Context, _ client.Host, c *client.Conn, s *client.Session) error {
				candidates, err := c.PolicyLearnCandidates(ctx, s.ID)
				if err != nil {
					return err
				}
				if dryRun {
					if asJSON {
						return printJSON(map[string]any{"dry_run": true, "candidates": candidates})
					}
					for _, candidate := range candidates {
						say(cmd, "%s (%d flows)\n", client.CleanLine(candidate.Target), candidate.Flows)
					}
					return nil
				}
				var picked []client.PolicyLearnCandidate
				for _, candidate := range candidates {
					choose := yes
					if !choose {
						choose, err = prompts(cmd).yesNo(fmt.Sprintf("Allow %s (%d flows) in this layer?", client.CleanLine(candidate.Target), candidate.Flows), false)
						if err != nil {
							return err
						}
					}
					if choose {
						picked = append(picked, candidate)
					}
				}
				finish, err := beginPolicyFileCommand(cmd, false)
				if err != nil {
					return err
				}
				defer finish()
				for _, candidate := range picked {
					rule := api.Rule{Allow: true, Door: api.DoorNet, Target: candidate.Target}
					if err := grantQuestions(cmd, func(ctx context.Context) error {
						_, err := client.ChangeFilePolicy(ctx, selected, rule, "", false)
						return err
					}); err != nil {
						return err
					}
				}
				result, err := propagateFilePolicy(cmd, selected)
				if err != nil {
					return err
				}
				if asJSON {
					return printJSON(map[string]any{"learned": picked, "propagation": result})
				}
				say(cmd, "Saved %d selected network rules.\n", len(picked))
				printFilePolicyPropagation(cmd, result)
				return nil
			})
		}}
	addAll(cmd, &layer.all, "save observed rules for all sessions")
	addProfile(cmd, &layer.profile, "save observed rules in this profile")
	cmd.Flags().StringVar(&layer.repo, "repo", "", "save observed rules for a repository origin or local folder")
	addDryRun(cmd, &dryRun, "show observed destinations and counts without writing or applying rules")
	addYes(cmd, &yes, "save every proposed rule")
	addJSON(cmd, &asJSON)
	return cmd
}
