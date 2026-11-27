package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// listPolicy keeps both reads on the same original accepted connection. A
// display host never becomes a target, and an old helper supplies no zero list.
func listPolicy(cmd *cobra.Command, layers policyLayerFlags, asJSON bool) error {
	if layers.in == "" {
		return withPolicy(cmd, layers, func(h client.Host, s client.Session, policy api.Policy) error {
			return printListedPolicy(h, s, policy, nil, asJSON, time.Now())
		})
	}
	if layers.count() != 1 {
		return policyUsage("a session policy cannot be mixed with file layers", "Pass --in alone, or combine --host, --repo and --profile.", "layer_conflict")
	}
	return withSession(cmd, layers.in, func(ctx context.Context, h client.Host, c *client.Conn, s *client.Session) error {
		policy, err := c.Policy(ctx, s.ID)
		if err != nil {
			return err
		}
		var pending *api.NetworkHoldSnapshot
		if c.HasFeature(api.FeatureNetworkHolds) {
			snapshot, err := c.PendingNetworkRequests(ctx, s.ID)
			if err != nil {
				return err
			}
			pending = &snapshot
		}
		return printListedPolicy(h, *s, policy, pending, asJSON, time.Now())
	})
}

func printListedPolicy(h client.Host, session client.Session, policy api.Policy, pending *api.NetworkHoldSnapshot, asJSON bool, now time.Time) error {
	if pending != nil && !api.ValidNetworkHoldSnapshot(*pending) {
		return &client.UserError{Code: "policy_pending_invalid", Kind: client.KindFailed,
			Msg: "the host returned an invalid pending network snapshot", Hint: "Upgrade the helper and read the pending requests again."}
	}
	if asJSON {
		out := policyObject(h, session)
		out["policy"] = policy
		if session.Wall != nil {
			out["wall"] = session.Wall
		}
		if pending != nil {
			out["pending_network"] = pending
		}
		return printJSON(out)
	}
	if pending == nil {
		printPolicy(session, policy)
		return nil
	}
	header := policy
	header.Rules = nil
	printPolicy(session, header)
	if pending.Stopped {
		fmt.Println("Held network requests: unavailable (sandbox stopped)")
	} else if len(pending.Requests) == 0 {
		fmt.Println("Held network requests: none")
	} else {
		fmt.Println("Held network requests:")
		if err := writePendingNetworkSnapshot(os.Stdout, *pending, now); err != nil {
			return err
		}
	}
	printPolicyRules(policy)
	return nil
}
