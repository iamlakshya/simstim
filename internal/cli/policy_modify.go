package cli

import (
	"context"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/grant"
)

func policyAllowCmd() *cobra.Command { return policyAddCmd(true) }
func policyDenyCmd() *cobra.Command  { return policyAddCmd(false) }

func policyAddCmd(allow bool) *cobra.Command {
	var layer policyLayerFlags
	var once, dryRun, asJSON bool
	var duration time.Duration
	verb := "deny"
	if allow {
		verb = "allow"
	}
	cmd := &cobra.Command{Use: verb + " [net] <target>", Short: "Add an " + verb + " rule at one policy layer", Args: cobra.RangeArgs(1, 2),
		Example: "  " + brand.Name + " policy " + verb + " api.example.com --repo .\n  " + brand.Name + " policy " + verb + " api.example.com --in box/auth-fix --dry-run",
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := client.ReadPolicySettings(); err != nil {
				return err
			}
			door, target, err := policyTarget(args)
			if err != nil {
				return err
			}
			canonical, _, err := grant.CheckHost(target)
			if err != nil {
				return policyUsage("invalid network target: "+client.CleanLine(err.Error()), "Pass a host, IP address or CIDR, with an optional port or path.", "bad_target")
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
			rule := api.Rule{Allow: allow, Door: door, Target: canonical, Once: once, Until: until}
			if layer.in == "" {
				selected, err := layer.file(cmd.Context())
				if err != nil {
					return err
				}
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
				if asJSON {
					return printJSON(map[string]any{"rule": result.Rule, "applies": result.Applies, "dry_run": dryRun})
				}
				say(cmd, "%s %s (%s, %s %s); applies at the next start.%s\n", verb, client.CleanLine(result.Rule.Target), client.CleanLine(result.Rule.ID), selected.Kind, client.CleanLine(selected.Name), dryPolicyNote(dryRun))
				return nil
			}
			return withSession(cmd, layer.in, func(ctx context.Context, h client.Host, c *client.Conn, s *client.Session) error {
				if allow {
					if err := grantQuestions(cmd, func(ctx context.Context) error { return c.CheckPolicyGrant(ctx, s.ID, canonical) }); err != nil {
						return err
					}
				}
				if dryRun {
					policy, err := c.Policy(ctx, s.ID)
					if err != nil {
						return err
					}
					if err := grant.CheckAddition(policy, rule); err != nil {
						return client.PolicyError(err)
					}
					rule.Layer = api.LayerSession
					rule.ID = api.RuleID(rule)
				} else {
					a := api.PolicyAddArgs{ID: s.ID, Allow: allow, Door: door, Target: canonical, Once: once, Until: until}
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
	cmd := &cobra.Command{Use: "rm <id or target>", Short: "Remove a rule from one policy layer", Args: cobra.ExactArgs(1), Example: "  " + brand.Name + " policy rm <rule-id> --repo .",
		RunE: func(cmd *cobra.Command, args []string) error {
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
				out, err := client.ChangeFilePolicy(cmd.Context(), selected, api.Rule{}, args[0], false)
				if err != nil {
					return err
				}
				if asJSON {
					return printJSON(map[string]any{"removed": out.Removed, "applies": "next_start"})
				}
				say(cmd, "Removed %s; applies at the next start.\n", client.CleanLine(args[0]))
				return nil
			}
			return withSession(cmd, layer.in, func(ctx context.Context, h client.Host, c *client.Conn, s *client.Session) error {
				policy, err := c.Policy(ctx, s.ID)
				if err != nil {
					return err
				}
				remove := api.PolicyRmArgs{Session: s.ID, Target: args[0]}
				for _, rule := range policy.Rules {
					if rule.ID == args[0] && rule.Layer == api.LayerSession {
						remove.ID, remove.Target = rule.ID, ""
						break
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
				say(cmd, "Revoked %s from %s.\n", client.CleanLine(args[0]), client.CleanLine(s.Name))
				return nil
			})
		}}
	layer.add(cmd)
	addJSON(cmd, &asJSON)
	return cmd
}
