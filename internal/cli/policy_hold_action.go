package cli

import (
	"context"
	"errors"
	"strings"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func heldNetworkIntentV1(layer policyLayerFlags, id string, once bool) (api.NetworkHoldActionArgs, client.FileLayer, error) {
	host, session, ok := strings.Cut(layer.in, "/")
	if !ok || !client.ValidHostName(host) || !api.ValidSessionID(session) || !api.ValidNetworkHoldID(id) || layer.all || layer.host != "" || layer.repo != "" && layer.profile != "" {
		return api.NetworkHoldActionArgs{}, client.FileLayer{}, policyUsage("a held action needs its exact session and request IDs", "Use --request with --in <host>/<session-ID>, and at most one of --repo or --profile.", "policy_pending_target")
	}
	args := api.NetworkHoldActionArgs{Version: api.NetworkHoldV1, Session: session, Request: id, Scope: api.NetworkHoldSession}
	var selected client.FileLayer
	switch {
	case layer.repo != "":
		origin, err := client.NormaliseOrigin(layer.repo)
		if err != nil {
			return args, selected, policyUsage("a held action needs an explicit repository origin", "Pass --repo with an origin such as forge.example/team/app.", "policy_pending_target")
		}
		args.Scope, selected = api.NetworkHoldRepo, client.FileLayer{Kind: api.LayerRepo, Name: origin}
	case layer.profile != "":
		args.Scope, selected = api.NetworkHoldProfile, client.FileLayer{Kind: api.LayerProfile, Name: layer.profile}
	case once:
		args.Scope = api.NetworkHoldOnce
	}
	if once && selected != (client.FileLayer{}) {
		return args, selected, policyUsage("a wider held answer cannot use --once", "Choose --once for the session, or one repository or profile without --once.", "layer_key")
	}
	return args, selected, nil
}

func runHeldNetworkAllowV1(cmd *cobra.Command, layer policyLayerFlags, id string, once, asJSON bool) (returned error) {
	args, selected, err := heldNetworkIntentV1(layer, id, once)
	if err != nil {
		return err
	}
	request, err := client.NewNetworkHoldActionRequestV1(args, selected)
	if err != nil {
		return err
	}
	if selected != (client.FileLayer{}) {
		finish, err := beginPolicyFileCommand(cmd, false)
		if err != nil {
			return err
		}
		defer finish()
	}
	ordinary := cmd.Context()
	cmd.SetContext(client.WithGrantRequest(client.WithoutPolicyReplay(ordinary), request))
	defer func() {
		returned = errors.Join(returned, request.Close())
		cmd.SetContext(ordinary)
	}()
	return withSession(cmd, layer.in, func(ctx context.Context, h client.Host, connection *client.Conn, session *client.Session) error {
		if session.ID != args.Session {
			return policyUsage("the selected session changed before the held action", "Read its pending requests again and use the exact session ID.", "policy_pending_target")
		}
		previous := cmd.Context()
		cmd.SetContext(ctx)
		defer cmd.SetContext(previous)
		var answer api.NetworkHoldAnswerResult
		var result client.FilePolicyResult
		if err := grantQuestions(cmd, func(ctx context.Context) error {
			var err error
			if selected == (client.FileLayer{}) {
				answer, err = connection.AnswerNetworkRequest(ctx, args)
			} else {
				result, err = connection.PublishNetworkRequest(ctx, args, selected)
			}
			return err
		}); err != nil {
			return err
		}
		if err := request.Close(); err != nil {
			return err
		}
		var propagation client.PolicyPropagationResult
		if selected != (client.FileLayer{}) {
			applyContext := ordinary
			if deadline, ok := ctx.Deadline(); ok {
				var cancel context.CancelFunc
				applyContext, cancel = context.WithDeadline(ordinary, deadline)
				defer cancel()
			}
			cmd.SetContext(applyContext)
			var err error
			propagation, err = propagateFilePolicy(cmd, selected)
			if err != nil {
				return err
			}
		}
		if asJSON {
			out := policyObject(h, *session)
			out["request"], out["scope"] = args.Request, args.Scope
			if selected == (client.FileLayer{}) {
				out["answer"] = answer
			} else {
				out["rule"], out["propagation"] = result.Rule, propagation
			}
			return printJSON(out)
		}
		say(cmd, "Allowed held request %s for %s (%s).\n", args.Request, client.CleanLine(session.Name), args.Scope)
		if selected != (client.FileLayer{}) {
			printFilePolicyPropagation(cmd, propagation)
		}
		return nil
	})
}

func policyRefuseCmdV1() *cobra.Command {
	var target string
	var asJSON bool
	cmd := &cobra.Command{Use: "refuse <request-ID>", Short: "Refuse one held network request by its opaque ID", Args: cobra.ExactArgs(1),
		Example: "  " + brand.Name + " policy refuse <request-ID> --in box/<session-ID>",
		RunE: func(cmd *cobra.Command, args []string) error {
			intent, _, err := heldNetworkIntentV1(policyLayerFlags{in: target}, args[0], false)
			if err != nil {
				return err
			}
			return withSession(cmd, target, func(ctx context.Context, h client.Host, connection *client.Conn, session *client.Session) error {
				if session.ID != intent.Session {
					return policyUsage("the selected session changed before the refusal", "Read its pending requests again and use the exact session ID.", "policy_pending_target")
				}
				if err := connection.RefuseNetworkRequest(ctx, intent.Session, intent.Request); err != nil {
					return err
				}
				if asJSON {
					out := policyObject(h, *session)
					out["refused"] = intent.Request
					return printJSON(out)
				}
				say(cmd, "Refused held request %s.\n", intent.Request)
				return nil
			})
		}}
	addIn(cmd, &target, "the exact host/session-ID whose request to refuse")
	addJSON(cmd, &asJSON)
	return cmd
}
