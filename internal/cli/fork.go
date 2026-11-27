package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func forkCmd() *cobra.Command {
	var count int
	var name string
	var detach, asJSON bool
	cmd := &cobra.Command{
		Use: "fork <host>/<session>", Short: "Copy a sandbox into independent sessions",
		Long: "Copies a sandbox's files and conversation into new sessions. Each child starts cold with its\n" +
			"own session identity and admission check. The source keeps running after the copy. A single\n" +
			"child attaches unless --detach is set. --count starts several children without attaching.",
		Example: "  " + brand.Name + " fork my-vps/auth-fix\n  " + brand.Name + " fork my-vps/auth-fix --count 3 --json",
		Args:    cobra.ExactArgs(1), ValidArgsFunction: completeSessionArg,
		RunE: func(cmd *cobra.Command, words []string) error {
			counted := cmd.Flags().Changed("count")
			if count < 1 || count > api.ForkMaxCount {
				return &client.UserError{Code: "bad_count", Msg: "fork count must be from 1 to 32", Hint: "Pass --count with a number from 1 to 32.", Kind: client.KindUsage}
			}
			if name != "" {
				if err := client.CheckSessionName(name); err != nil {
					return err
				}
			}
			if _, err := client.ReadPolicySettings(); err != nil {
				return err
			}
			if err := beforeAttach(cmd, asJSON, detach || counted); err != nil {
				return err
			}
			return withSession(cmd, words[0], func(ctx context.Context, h client.Host, c *client.Conn, s *client.Session) error {
				forkCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
				defer cancel()
				result, err := c.Fork(forkCtx, api.ForkArgs{ID: s.ID, Count: count, Name: name})
				if err != nil {
					return err
				}
				for _, child := range result.Children {
					client.RememberSession(h.Name, child)
				}
				if asJSON {
					children := make([]any, 0, len(result.Children))
					for _, child := range result.Children {
						children = append(children, sessionObject(h.Name, child))
					}
					type childFailure struct {
						Index int       `json:"index"`
						Error errorJSON `json:"error"`
					}
					failures := make([]childFailure, 0, len(result.Failures))
					for _, failure := range result.Failures {
						r := describe(client.ForkFailureError(failure), cmd)
						failures = append(failures, childFailure{failure.Index, errorJSON{Code: r.code, Kind: r.kind.String(), Message: r.msg, Try: r.try, Exit: exitCode(r.kind), Suggestions: r.suggestions, Data: validJSONData(r.data)}})
					}
					if err := printJSON(struct {
						Version  int            `json:"v"`
						Host     string         `json:"host"`
						Parent   string         `json:"parent"`
						Children []any          `json:"children"`
						Failures []childFailure `json:"failures,omitempty"`
					}{1, h.Name, result.Parent, children, failures}); err != nil {
						return err
					}
				} else {
					for _, child := range result.Children {
						say(cmd, "Forked %s as %s (%s) on %s.\n", shownShort(s.Name), shownShort(child.Name), shown(child.ID), shownShort(h.Name))
					}
					for _, failure := range result.Failures {
						fmt.Fprintf(cmd.ErrOrStderr(), "Child %d: %s\nTry: %s\n", failure.Index, client.CleanLine(failure.Error.Message), client.CleanLine(failure.Error.Hint))
					}
				}
				if len(result.Failures) > 0 {
					failure := client.ForkFailureError(result.Failures[0])
					if asJSON || len(result.Children) > 0 {
						r := describe(failure, cmd)
						return &quietError{&client.UserError{Msg: r.msg, Hint: r.try, Code: r.code, Kind: r.kind}}
					}
					return failure
				}
				if count == 1 && !detach && !counted {
					child := result.Children[0]
					return attach(ctx, h, child.ID, child.Name)
				}
				return nil
			})
		},
	}
	addCount(cmd, &count, "number of independent children to start (1 to 32)")
	addName(cmd, &name, "name for the child, or the base name for several children")
	addDetach(cmd, &detach, "start the child without attaching")
	addJSON(cmd, &asJSON)
	return cmd
}
