package cli

import (
	"context"
	"errors"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// A fresh member changes only its selected agent/command and terminal. It
// cannot reinterpret a root's mounts, policy, image, input or resource pins.
func checkFreshMemberFlags(cmd *cobra.Command) error {
	allowed := map[string]bool{"in": true, "agent": true, "name": true, "task": true, "detach": true, "json": true}
	var refused string
	cmd.LocalFlags().Visit(func(flag *pflag.Flag) {
		if !allowed[flag.Name] && refused == "" {
			refused = flag.Name
		}
	})
	if refused != "" {
		return policyUsage("--"+refused+" changes the sandbox and cannot be used with new --in", "Use --agent, --name, --task, --detach or a command after --. Start a new sandbox to change its settings.", "member_flag")
	}
	return nil
}

func runFreshMemberNew(cmd *cobra.Command, args []string, parent, agent, name, task string, detach, asJSON bool) (returned error) {
	if err := checkFreshMemberFlags(cmd); err != nil {
		return err
	}
	if strings.TrimSpace(parent) == "" {
		return policyUsage("--in needs the original sandbox session", "Pass new <host> --in <session>.", "session_missing")
	}
	if err := beforeAttach(cmd, asJSON, detach); err != nil {
		return err
	}
	host, err := selectedNewHost(cmd, args)
	if err != nil {
		return err
	}
	address := host.Name + "/" + parent
	if selectedHost, _, qualified := strings.Cut(parent, "/"); qualified {
		if selectedHost != host.Name {
			return policyUsage("the --in session belongs to another host", "Use the same saved host and its complete session address.", "host_conflict")
		}
		address = parent
	}
	cols, rows := termSize()
	choice := api.JoinArgs{Name: name, Agent: agent, Task: task, Cols: cols, Rows: rows}
	if dash := cmd.ArgsLenAtDash(); dash >= 0 {
		choice.Argv = append([]string{}, args[dash:]...)
	}
	request := client.NewFreshChildGrantRequest()
	original := cmd.Context()
	cleanup := &newStartCleanup{request: request, budget: original}
	bound := client.WithGrantRequest(client.WithoutWake(original), request)
	cmd.SetContext(context.WithValue(bound, newStartCleanupKey{}, cleanup))
	defer func() { returned = errors.Join(returned, cleanup.finish()); cmd.SetContext(original) }()
	return withSession(cmd, address, func(ctx context.Context, h client.Host, conn *client.Conn, parent *client.Session) error {
		before := cmd.Context()
		cmd.SetContext(ctx)
		defer cmd.SetContext(before)
		recordNewStartBudget(cmd.Context(), ctx)
		choice.Parent = parent.ID // live resolution restricts the local factory
		var child *client.Session
		if err := grantQuestions(cmd, func(ctx context.Context) error {
			var err error
			child, err = conn.StartFreshChild(ctx, choice, secretTerminal(cmd))
			return err
		}); err != nil {
			return err
		}
		if err := cleanup.finish(); err != nil {
			return err
		}
		cmd.SetContext(original)
		client.RememberSession(h.Name, *child)
		wallStartNotes(cmd, *child)
		if !detach {
			conn.Close()
			return attach(original, h, child.ID, child.Name)
		}
		if asJSON {
			return printJSON(sessionObject(h.Name, *child))
		}
		say(cmd, "Started %s/%s (%s, %s).\n", shown(h.Name), shown(child.ID), shown(child.Name), shown(child.Agent))
		return nil
	})
}
