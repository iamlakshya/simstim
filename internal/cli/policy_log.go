package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func policyLogCmd() *cobra.Command {
	var target, door, since, by string
	var asJSON, follow bool
	cmd := &cobra.Command{
		Use: "log --in <host>/<session>", Short: "Read one session's network decisions",
		Example: "  " + brand.Name + " policy log --in my-vps/auth-fix --since 1h\n" +
			"  " + brand.Name + " policy log --in my-vps/auth-fix --follow --json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := sessionLayer(target); err != nil {
				return err
			}
			if door != "net" {
				return &client.UserError{Msg: "only net decisions are available in this version",
					Hint: "Pass --door net, or leave --door out.", Code: "bad_door", Kind: client.KindUsage}
			}
			if by != "" && by != "classifier" {
				return &client.UserError{Msg: "--by accepts classifier only",
					Hint: "Leave --by out to see every decision.", Code: "policy_filter", Kind: client.KindUsage}
			}
			if err := policySince(since); err != nil {
				return err
			}
			return withSession(cmd, target, func(_ context.Context, h client.Host, c *client.Conn, s *client.Session) error {
				// A relative start time is pinned once. Re-evaluating it on each
				// poll would silently move the beginning of a followed log.
				start := since
				if d, err := time.ParseDuration(since); err == nil {
					start = time.Now().Add(-d).UTC().Format(time.RFC3339)
				}
				a := api.PolicyLogArgs{ID: s.ID, Door: api.DoorNet, Since: start, By: by}
				if !follow {
					ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
					defer cancel()
					entries, err := c.PolicyLog(ctx, a)
					if err != nil {
						return err
					}
					if asJSON {
						out := policyObject(h, *s)
						out["entries"] = entries
						return printJSON(out)
					}
					return printPolicyEntries(cmd.Context(), entries, false, h, *s)
				}
				for {
					ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
					page, err := c.PolicyLogPage(ctx, a)
					cancel()
					if err != nil {
						return err
					}
					if err := printPolicyEntries(cmd.Context(), page.Entries, asJSON, h, *s); err != nil {
						return err
					}
					a.Cursor = page.Cursor
					if page.More {
						continue
					}
					timer := time.NewTimer(time.Second)
					select {
					case <-cmd.Context().Done():
						timer.Stop()
						return cmd.Context().Err()
					case <-timer.C:
					}
				}
			})
		},
	}
	addIn(cmd, &target, "the session whose decisions to read")
	cmd.Flags().StringVar(&door, "door", "net", "the door to read (net)")
	cmd.Flags().StringVar(&since, "since", "", "read from a timestamp or a duration such as 1h")
	cmd.Flags().StringVar(&by, "by", "", "read classifier decisions only")
	addFollow(cmd, &follow, "poll the host's audit log for new decisions")
	addJSON(cmd, &asJSON)
	return cmd
}

func policySince(value string) error {
	if value == "" {
		return nil
	}
	if _, err := time.Parse(time.RFC3339, value); err == nil {
		return nil
	}
	if d, err := time.ParseDuration(value); err == nil && d >= 0 {
		return nil
	}
	return &client.UserError{Msg: "invalid policy log start time",
		Hint: "Pass an RFC 3339 timestamp or a duration such as 1h.", Code: "policy_filter", Kind: client.KindUsage}
}

func printPolicyEntries(ctx context.Context, entries []client.PolicyLogEntry, asJSON bool, h client.Host, s client.Session) error {
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if asJSON {
			out := policyObject(h, s)
			out["v"], out["entry"] = jsonVersion, e
			b, err := json.Marshal(out)
			if err != nil {
				return fmt.Errorf("encode policy log: %w", err)
			}
			if _, err := os.Stdout.Write(append(jsonSafe(b), '\n')); err != nil {
				return fmt.Errorf("write policy log: %w", err)
			}
			continue
		}
		if _, err := fmt.Fprintf(os.Stdout, "%s  %s %s:%d  %s  %s\n", client.CleanLine(e.T), client.CleanLine(e.Decision), client.CleanLine(e.Host), e.Port, client.CleanLine(e.Rule), client.CleanLine(e.Path)); err != nil {
			return fmt.Errorf("write policy log: %w", err)
		}
	}
	return nil
}
