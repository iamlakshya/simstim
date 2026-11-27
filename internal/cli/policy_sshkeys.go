package cli

import (
	"context"
	"errors"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

func runNamedSSHKeyPolicy(cmd *cobra.Command, layer policyLayerFlags, name string, allow, once, dryRun, asJSON, public bool, tools []string) (returned error) {
	if layer.in == "" || layer.count() != 1 {
		return policyUsage("named SSH key changes need one live session", "Pass --in <host>/<session> with policy allow ssh-key or policy deny ssh-key.", "layer_missing")
	}
	if api.CheckSecretName(name, false) != api.SecretNameOK {
		return policyUsage("the SSH key name is invalid", "Name one SSH key configured in the local secrets table.", "bad_secret")
	}
	if once || public || len(tools) > 0 || cmd.Flags().Changed("tools") || cmd.Flags().Changed("for") {
		return policyUsage("SSH key changes do not take network rule flags", "Leave out --once, --public, --tools and --for.", "bad_door")
	}
	request := client.NewNamedSSHKeyGrantRequest()
	original := cmd.Context()
	cmd.SetContext(client.WithGrantRequest(original, request))
	defer func() { returned = errors.Join(returned, request.Close()); cmd.SetContext(original) }()
	return withSession(cmd, layer.in, func(ctx context.Context, h client.Host, conn *client.Conn, session *client.Session) error {
		before := cmd.Context()
		cmd.SetContext(ctx)
		defer cmd.SetContext(before)
		var result api.NamedSSHKeyPolicyResult
		if dryRun {
			if err := grantQuestions(cmd, func(ctx context.Context) error { return conn.CheckNamedSSHKeyPolicy(ctx, session.ID, name, allow) }); err != nil {
				return err
			}
		} else {
			if err := grantQuestions(cmd, func(ctx context.Context) error {
				var err error
				result, err = conn.ChangeNamedSSHKeyPolicy(ctx, session.ID, name, allow)
				return err
			}); err != nil {
				return err
			}
		}
		verb := "deny"
		if allow {
			verb = "allow"
		}
		if asJSON {
			out := policyObject(h, *session)
			out["door"], out["name"], out["allow"] = api.DoorSSHKey, name, allow
			out["dry_run"] = dryRun
			if !dryRun {
				out["named_ssh_keys"] = result.Names
				out["applies"] = "next_start"
			}
			return printJSON(out)
		}
		if dryRun {
			say(cmd, "%s ssh-key %s for %s (dry run).\n", verb, name, client.CleanLine(session.Name))
			return nil
		}
		say(cmd, "%s ssh-key %s for %s. It applies at the next start.\n", verb, name, client.CleanLine(session.Name))
		return nil
	})
}
