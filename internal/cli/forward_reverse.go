package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

func selectForwardPort(cmd *cobra.Command, conn *client.Conn, host client.Host, session *client.Session) (int, error) {
	ports, err := sessionPorts(cmd.Context(), conn, session.ID)
	if err != nil {
		return 0, err
	}
	if len(ports) == 0 {
		return 0, &client.UserError{Msg: "the sandbox claims no listening ports", Hint: sessionCommand(host.Name, session.ID, "forward", "3000"), Code: "no_ports", Kind: client.KindNotFound}
	}
	if !inputOn(cmd) {
		return 0, &client.UserError{Msg: "forward needs an explicit sandbox port when input is off", Hint: sessionCommand(host.Name, session.ID, "forward", "3000"), Code: "port_required", Kind: client.KindNeedsAnswer}
	}
	for _, port := range ports {
		label := "claimed"
		if port.Granted {
			label += ", granted"
		}
		say(cmd, "%d  %s (%s)\n", port.Port, shown(port.Process), label)
	}
	prompt := prompts(cmd)
	for attempt := 0; attempt < 3; attempt++ {
		answer, err := prompt.ask("Port to forward (Enter cancels): ")
		if err != nil {
			return 0, err
		}
		if answer == "" {
			return 0, prompt.cancel()
		}
		selected, err := api.ParsePortTarget(answer)
		if err == nil {
			for _, port := range ports {
				if port.Port == selected {
					return selected, nil
				}
			}
		}
		fmt.Fprintln(prompt.out, "Choose one of the listed ports, or press Enter to cancel.")
	}
	return 0, prompt.cancel()
}

func runReverseForward(cmd *cobra.Command, args []string, target string) error {
	local, remote, pair := strings.Cut(target, ":")
	if !pair {
		remote = local
	}
	laptopPort, first := api.ParsePortTarget(local)
	guestPort, second := api.ParsePortTarget(remote)
	if len(args) != 1 || first != nil || second != nil {
		return &client.UserError{Msg: "reverse forwarding needs one laptop port or laptop:guest pair", Hint: "Pass --reverse 5432, or --reverse 5432:6543, after one sandbox address.", Code: "bad_port", Kind: client.KindUsage}
	}
	return withSession(cmd, args[0], func(ctx context.Context, host client.Host, conn *client.Conn, session *client.Session) error {
		if session.Wall == nil {
			return &client.UserError{Msg: "reverse forwarding needs a sandbox", Hint: "Start or resume a walled session on an own host.", Code: "not_walled", Kind: client.KindRefused}
		}
		return conn.RunReverse(ctx, session.ID, laptopPort, guestPort, func() {
			fmt.Fprintf(os.Stderr, "Guest 127.0.0.1:%d → laptop 127.0.0.1:%d\nPress Ctrl+C to stop.\n", guestPort, laptopPort)
		})
	})
}
