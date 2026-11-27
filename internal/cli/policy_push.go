package cli

import (
	"context"
	"errors"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/grant"
)

// The command owns this original positive flag intent before any question.
// The returned host policy and Rule.Mode cannot manufacture --public consent.
func runPositivePushPolicy(cmd *cobra.Command, layer policyLayerFlags, rawTarget string, publicFlag, once, dryRun, asJSON bool, duration time.Duration) (returned error) {
	if layer.in == "" || layer.count() != 1 {
		return policyUsage("push allowances need one session layer", "Pass --in <host>/<session> with policy allow push.", "layer_missing")
	}
	if cmd.Flags().Changed("tools") {
		return policyUsage("push allowances do not take model tools", "Leave out --tools for policy allow push.", "bad_push")
	}
	if _, err := client.NormalizeRepo(rawTarget); err != nil {
		return err
	}
	value := ""
	if cmd.Flags().Changed("for") {
		value = duration.String()
	}
	// Materialise the full UTC deadline once. Retrying a question never
	// renews --for or derives an argument from a returned policy row.
	until, err := policyUntil(value, time.Now())
	if err != nil {
		return err
	}
	mode := ""
	if publicFlag {
		mode = api.PushModePublic
	}
	request := client.NewPushGrantRequest()
	original := cmd.Context()
	cmd.SetContext(client.WithGrantRequest(original, request))
	defer func() {
		returned = errors.Join(returned, request.Close())
		cmd.SetContext(original)
	}()
	return withSession(cmd, layer.in, func(ctx context.Context, h client.Host, conn *client.Conn, session *client.Session) error {
		// Keep the original request lifetime, while this actual attempt also
		// obeys withSession's deadline through questions and synchronous IO.
		before := cmd.Context()
		cmd.SetContext(ctx)
		defer cmd.SetContext(before)
		args := api.PolicyAddArgs{ID: session.ID, Allow: true, Door: api.DoorPush, Target: rawTarget, Mode: mode, Once: once, Until: until}
		var rule api.Rule
		if dryRun {
			if err := grantQuestions(cmd, func(ctx context.Context) error {
				return conn.CheckPushGrant(ctx, args, publicFlag)
			}); err != nil {
				return err
			}
			policy, err := conn.Policy(cmd.Context(), session.ID)
			if err != nil {
				return err
			}
			target, err := client.NormalizeRepo(rawTarget)
			if err != nil {
				return err
			}
			rule = api.Rule{Allow: true, Door: api.DoorPush, Target: target, Mode: mode, Once: once, Until: until, Layer: api.LayerSession}
			rule.ID = api.RuleID(rule)
			if err := grant.CheckAddition(policy, rule); err != nil {
				return client.PolicyError(err)
			}
		} else {
			if err := grantQuestions(cmd, func(ctx context.Context) error {
				var err error
				rule, err = conn.AllowPushPolicy(ctx, args, publicFlag)
				return err
			}); err != nil {
				return err
			}
		}
		if asJSON {
			out := policyObject(h, *session)
			out["rule"], out["applies"], out["dry_run"] = rule, "at_once", dryRun
			return printJSON(out)
		}
		say(cmd, "allow push %s for %s (%s); applies at once.%s\n", client.CleanLine(rule.Target), client.CleanLine(session.Name), client.CleanLine(rule.ID), dryPolicyNote(dryRun))
		return nil
	})
}
