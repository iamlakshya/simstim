package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func shellCmd() *cobra.Command {
	var forceNew, detach, asJSON bool
	var name string
	cmd := &cobra.Command{
		Use:   "shell <host>/<session>",
		Short: "Open a shell beside a session",
		Long: "Attaches to the newest running shell in the same sandbox, or starts one. --new always starts\n" +
			"a shell. --name starts a named shell. At none, a new host shell uses the session's folder.\n" +
			"Sandbox files reach the laptop through pull and its review, even when a shell wrote them.",
		Example:           "  " + brand.Name + " shell my-vps/auth-fix\n  " + brand.Name + " shell my-vps/auth-fix --new --name inspect",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeSessionArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := client.CheckSessionName(name); err != nil {
				return err
			}
			if err := beforeAttach(cmd, asJSON, detach); err != nil {
				return err
			}
			return withSession(cmd, args[0], func(ctx context.Context, h client.Host, conn *client.Conn, parent *client.Session) (returned error) {
				local, err := client.LoadSandboxPolicy(ctx, h, parent.ID)
				if err != nil {
					return err
				}
				if local == nil || local.HostID != conn.HostID() || local.FutureLineage || local.FutureDelivery {
					return &client.UserError{Msg: "the session has no matching original local creation record", Hint: "Open a session created by this laptop, or start a new shell with new on this host.", Code: "grant_origin", Kind: client.KindRefused}
				}
				unwalled := local.CreationWall == "none"
				rows, err := conn.Sessions(ctx)
				if err != nil {
					return err
				}
				if !unwalled && !forceNew && name == "" {
					snapshot, err := conn.SandboxSnapshot(ctx, parent.ID)
					if err != nil {
						return err
					}
					candidates := runningShellRowsV1(rows, snapshot)
					for _, candidate := range candidates {
						allowed, err := conn.SameShellSandboxV1(ctx, parent.ID, candidate.ID)
						if err != nil {
							return err
						}
						if !allowed {
							continue
						}
						client.RememberSession(h.Name, candidate)
						if detach {
							if asJSON {
								return printJSON(sessionObject(h.Name, candidate))
							}
							say(cmd, "Shell %s/%s (%s) keeps running.\n", shown(h.Name), shown(candidate.ID), shown(candidate.Name))
							return nil
						}
						conn.Close()
						return attach(cmd.Context(), h, candidate.ID, candidate.Name)
					}
				}
				original := cmd.Context()
				var request *client.GrantRequest
				if unwalled {
					request = client.NewGrantRequest(false, false)
				} else {
					request = client.NewFreshChildGrantRequest()
				}
				cleanup := &newStartCleanup{request: request, budget: ctx}
				bound := client.WithGrantRequest(ctx, request)
				cmd.SetContext(context.WithValue(bound, newStartCleanupKey{}, cleanup))
				defer func() { returned = errors.Join(returned, cleanup.finish()); cmd.SetContext(original) }()
				cols, lines := termSize()
				var shell *client.Session
				if unwalled {
					chosen := name
					if chosen == "" {
						chosen = nextHostShellNameV1(parent.Name, rows)
					}
					args := client.NewArgs{Agent: "shell", Name: chosen, Cwd: parent.Cwd, Wall: "none", Downgrade: true, Cols: cols, Rows: lines}
					if err := grantQuestions(cmd, func(question context.Context) error {
						before := cmd.Context()
						cmd.SetContext(question)
						defer cmd.SetContext(before)
						var err error
						shell, err = startSession(cmd, conn, h, args)
						return err
					}); err != nil {
						return err
					}
				} else {
					choice := api.JoinArgs{Parent: parent.ID, Agent: "shell", Name: name, Cols: cols, Rows: lines}
					if err := grantQuestions(cmd, func(ctx context.Context) error {
						var err error
						shell, err = conn.StartFreshChild(ctx, choice, secretTerminal(cmd))
						return err
					}); err != nil {
						return err
					}
				}
				if err := cleanup.finish(); err != nil {
					return err
				}
				client.RememberSession(h.Name, *shell)
				wallStartNotes(cmd, *shell)
				if detach {
					if asJSON {
						return printJSON(sessionObject(h.Name, *shell))
					}
					say(cmd, "Started shell %s/%s (%s).\n", shown(h.Name), shown(shell.ID), shown(shell.Name))
					return nil
				}
				conn.Close()
				return attach(original, h, shell.ID, shell.Name)
			})
		},
	}
	cmd.Flags().BoolVar(&forceNew, "new", false, "start a new shell even when one already runs")
	cmd.Flags().StringVarP(&name, "name", "n", "", "name the new shell session")
	cmd.Flags().BoolVar(&detach, "detach", false, "leave the shell running without attaching")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the detached shell as JSON")
	return cmd
}

func runningShellRowsV1(rows []client.Session, snapshot *api.SandboxSnapshot) []client.Session {
	if snapshot == nil {
		return nil
	}
	var selected []client.Session
	for _, row := range rows {
		if row.Agent == "shell" && row.Alive && !client.Ended(row) && (row.ID == snapshot.Primary || row.SandboxOf == snapshot.Primary && slices.Contains(snapshot.Members, row.ID)) {
			selected = append(selected, row)
		}
	}
	slices.SortFunc(selected, func(a, b client.Session) int {
		if order := b.CreatedAt.Compare(a.CreatedAt); order != 0 {
			return order
		}
		if a.ID < b.ID {
			return 1
		}
		if a.ID > b.ID {
			return -1
		}
		return 0
	})
	return selected
}

func nextHostShellNameV1(parent string, rows []client.Session) string {
	used := make(map[string]bool, len(rows))
	for _, row := range rows {
		used[row.Name] = true
	}
	if parent == "" {
		parent = "session"
	}
	for index := 1; ; index++ {
		suffix := "-sh"
		if index > 1 {
			suffix = fmt.Sprintf("-sh%d", index)
		}
		runes := []rune(parent)
		limit := api.SessionNameMax - len([]rune(suffix))
		if len(runes) > limit {
			runes = runes[:limit]
		}
		name := string(runes) + suffix
		if !used[name] {
			return name
		}
	}
}
