package cli

import (
	"context"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/grant"
)

func policyAllowCmd() *cobra.Command { return policyAddCmd(true) }
func policyDenyCmd() *cobra.Command  { return policyAddCmd(false) }

func policyAddCmd(allow bool) *cobra.Command {
	var layer policyLayerFlags
	var requestID string
	var once, dryRun, asJSON, publicPush bool
	var duration time.Duration
	var tools []string
	verb := "deny"
	if allow {
		verb = "allow"
	}
	cmd := &cobra.Command{Use: verb + " [net] <target>", Short: "Add an " + verb + " rule at one policy layer", Args: func(cmd *cobra.Command, args []string) error {
		if requestID != "" {
			if len(args) != 0 {
				return policyUsage("a held answer takes no target argument", "Use --request with --in and the original request ID, without a target.", "flags_conflict")
			}
			return nil
		}
		return cobra.RangeArgs(1, 2)(cmd, args)
	},
		Example: "  " + brand.Name + " policy " + verb + " api.example.com --repo .\n  " + brand.Name + " policy " + verb + " api.example.com --in box/auth-fix --dry-run",
		RunE: func(cmd *cobra.Command, args []string) error {
			restoreReplay := noPolicyReplayForDryRun(cmd, dryRun)
			defer restoreReplay()
			if _, err := client.ReadPolicySettings(); err != nil {
				return err
			}
			if requestID != "" {
				if !allow || publicPush || cmd.Flags().Changed("tools") || cmd.Flags().Changed("for") || dryRun {
					return policyUsage("a held answer cannot take target-grant or preview options", "Use --request with --in and --once, --repo or --profile; read policy ls to preview held requests.", "flags_conflict")
				}
				return runHeldNetworkAllowV1(cmd, layer, requestID, once, asJSON)
			}
			door, target, err := policyTarget(args)
			if err != nil {
				return err
			}
			if door == api.DoorSSHKey {
				return runNamedSSHKeyPolicy(cmd, layer, target, allow, once, dryRun, asJSON, publicPush, tools)
			}
			if door == api.DoorPush {
				if !allow {
					return policyUsage("this command supports positive push allowances", "Use policy allow push with --in, or revoke an existing rule with policy rm.", "bad_push")
				}
				return runPositivePushPolicy(cmd, layer, target, publicPush, once, dryRun, asJSON, duration)
			}
			if publicPush {
				return policyUsage("--public belongs to a push allowance", "Use policy allow push <repository> --in <host>/<session> --public.", "bad_push")
			}
			canonical := target
			if door == api.DoorPort {
				if _, err := api.ParsePortTarget(target); err != nil {
					return policyUsage("invalid exact port", "Name one decimal port from 1 to 65535.", "bad_port")
				}
				if layer.in == "" || once || len(tools) != 0 || !allow && cmd.Flags().Changed("for") {
					return policyUsage("port rules need a supported session scope", "Use --in with one exact port, without --once or --tools.", "bad_port")
				}
			} else {
				canonical, _, err = grant.CheckHost(target)
				if err != nil {
					return policyUsage("invalid network target: "+client.CleanLine(err.Error()), "Pass a host, IP address or CIDR, with an optional port or path.", "bad_target")
				}
			}
			selectedTools, err := api.CanonicalModelTools(tools)
			if err != nil {
				return client.PolicyError(err)
			}
			if cmd.Flags().Changed("tools") && (len(selectedTools) == 0 || !allow || layer.in == "") {
				return policyUsage("--tools needs a non-empty session allowance", "Pass --in and one or more named capabilities to policy allow.", "bad_tools")
			}
			if err := layer.mutation(cmd); err != nil {
				return err
			}
			if layer.in == "" && (once || cmd.Flags().Changed("for")) {
				return policyUsage("--once and --for require the session layer", "Pass --in <host>/<session>.", "layer_key")
			}
			value := ""
			if cmd.Flags().Changed("for") {
				value = duration.String()
			}
			until, err := policyUntil(value, time.Now())
			if err != nil {
				return err
			}
			rule := api.Rule{Allow: allow, Door: door, Target: canonical, Once: once, Until: until, Tools: selectedTools}
			if len(rule.Tools) > 0 {
				if err := egress.CheckRuleAddition(api.Policy{}, rule); err != nil {
					return client.PolicyError(err)
				}
			}
			if layer.in == "" {
				selected, err := layer.file(cmd.Context())
				if err != nil {
					return err
				}
				finishPolicy, err := beginPolicyFileCommand(cmd, dryRun)
				if err != nil {
					return err
				}
				defer finishPolicy()
				var result client.FilePolicyResult
				if allow && !dryRun {
					err = grantQuestions(cmd, func(ctx context.Context) error {
						var changeErr error
						result, changeErr = client.ChangeFilePolicy(ctx, selected, rule, "", false)
						return changeErr
					})
				} else {
					result, err = client.ChangeFilePolicy(cmd.Context(), selected, rule, "", dryRun)
				}
				if err != nil {
					return err
				}
				var propagation client.PolicyPropagationResult
				if !dryRun {
					propagation, err = propagateFilePolicy(cmd, selected)
					if err != nil {
						return err
					}
				}
				if asJSON {
					return printJSON(map[string]any{"rule": result.Rule, "applies": result.Applies, "dry_run": dryRun, "propagation": propagation})
				}
				say(cmd, "%s %s (%s, %s %s); saved to its policy file.%s\n", verb, client.CleanLine(result.Rule.Target), client.CleanLine(result.Rule.ID), selected.Kind, client.CleanLine(selected.Name), dryPolicyNote(dryRun))
				if !dryRun {
					printFilePolicyPropagation(cmd, propagation)
				}
				return nil
			}
			return withSession(cmd, layer.in, func(ctx context.Context, h client.Host, c *client.Conn, s *client.Session) error {
				if allow {
					if err := grantQuestions(cmd, func(ctx context.Context) error {
						if door == api.DoorPort {
							return c.CheckPortPolicyGrant(ctx, s.ID)
						}
						return c.CheckPolicyGrant(ctx, s.ID, canonical)
					}); err != nil {
						return err
					}
				}
				if dryRun {
					if err := c.CheckHTTPMethodFeature([]api.Layer{{Kind: api.LayerSession, Rules: []api.Rule{rule}}}); err != nil {
						return err
					}
					policy, err := c.Policy(ctx, s.ID)
					if err != nil {
						return err
					}
					rule.Layer = api.LayerSession
					rule.ID = api.RuleID(rule)
					if err := grant.CheckAddition(policy, rule); err != nil {
						return client.PolicyError(err)
					}
				} else {
					a := api.PolicyAddArgs{ID: s.ID, Allow: allow, Door: door, Target: canonical, Once: once, Until: until, Tools: selectedTools}
					if allow {
						err = grantQuestions(cmd, func(ctx context.Context) error { rule, err = c.AllowPolicy(ctx, a); return err })
					} else {
						rule, err = c.DenyPolicy(ctx, a)
					}
					if err != nil {
						return err
					}
				}
				if asJSON {
					out := policyObject(h, *s)
					out["rule"], out["applies"], out["dry_run"] = rule, "at_once", dryRun
					return printJSON(out)
				}
				say(cmd, "%s %s for %s (%s); applies at once.%s\n", verb, client.CleanLine(rule.Target), client.CleanLine(s.Name), client.CleanLine(rule.ID), dryPolicyNote(dryRun))
				return nil
			})
		}}
	layer.add(cmd)
	addDryRun(cmd, &dryRun, "show the rule without writing or sending a mutation")
	if allow {
		cmd.Use = "allow [net] [<target>]"
		cmd.Long = "Allow a destination at one selected policy layer. With --request, answer an original held request by opaque ID and exact session ID. Upload budget holds support only --once or policy refuse; change network.upload_budget through policy edit for a higher limit."
		cmd.Example += "\n  " + brand.Name + " policy allow --request <request-ID> --in box/<session-ID> --once"
		cmd.Flags().StringVar(&requestID, "request", "", "answer a live held network request by its opaque ID; upload budgets support only --once")
		cmd.Flags().BoolVar(&publicPush, "public", false, "acknowledge that this exact repository push publishes to a public repository")
		addList(cmd.Flags(), &tools, "tools", "allow model capabilities for this session: a comma-separated list, or the flag again")
		cmd.Flags().BoolVar(&once, "once", false, "allow one matching flow, at session scope")
		addFor(cmd, &duration, 0, "keep a session rule for a duration, such as 1h")
		cmd.MarkFlagsMutuallyExclusive("once", "for")
	}
	addJSON(cmd, &asJSON)
	return cmd
}

func dryPolicyNote(dry bool) string {
	if dry {
		return " Dry run: nothing changed."
	}
	return ""
}

func policyRmCmd() *cobra.Command {
	var layer policyLayerFlags
	var asJSON bool
	cmd := &cobra.Command{Use: "rm <id or target>", ValidArgsFunction: firstArg(completeRuleIDs), Short: "Remove a rule from one policy layer", Args: cobra.ExactArgs(1), Example: "  " + brand.Name + " policy rm <rule-id> --repo .",
		RunE: func(cmd *cobra.Command, args []string) error {
			word := strings.TrimSpace(args[0])
			if _, err := client.ReadPolicySettings(); err != nil {
				return err
			}
			if err := layer.mutation(cmd); err != nil {
				return err
			}
			if layer.in == "" {
				selected, err := layer.file(cmd.Context())
				if err != nil {
					return err
				}
				finishPolicy, err := beginPolicyFileCommand(cmd, false)
				if err != nil {
					return err
				}
				defer finishPolicy()
				out, err := client.ChangeFilePolicy(cmd.Context(), selected, api.Rule{}, word, false)
				if err != nil {
					return err
				}
				propagation, err := propagateFilePolicy(cmd, selected)
				if err != nil {
					return err
				}
				if asJSON {
					return printJSON(map[string]any{"removed": out.Removed, "applies": "next_start", "propagation": propagation})
				}
				say(cmd, "Removed %s from its policy file.\n", client.CleanLine(word))
				printFilePolicyPropagation(cmd, propagation)
				return nil
			}
			return withSession(cmd, layer.in, func(ctx context.Context, h client.Host, c *client.Conn, s *client.Session) error {
				remove := api.PolicyRmArgs{Session: s.ID, Target: word}
				if api.IsRuleID(word) {
					policy, err := c.Policy(ctx, s.ID)
					if err != nil {
						return err
					}
					found, err := egress.CheckRuleIDSelection(policy.Rules, word, api.LayerRef{Kind: api.LayerSession})
					if err != nil {
						return client.PolicyError(err)
					}
					if found {
						remove.ID, remove.Target = word, ""
					}
				}
				removed, err := c.RemovePolicy(ctx, remove)
				if err != nil {
					return err
				}
				if asJSON {
					out := policyObject(h, *s)
					out["removed"] = removed
					return printJSON(out)
				}
				say(cmd, "Revoked %s from %s.\n", client.CleanLine(word), client.CleanLine(s.Name))
				return nil
			})
		}}
	layer.add(cmd)
	addJSON(cmd, &asJSON)
	return cmd
}
