package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func policyLockdownCmd() *cobra.Command {
	var hostName, literal string
	var off, asJSON bool
	cmd := &cobra.Command{Use: "lockdown", Short: "Pause session allowances and refuse unknown network destinations", Args: cobra.NoArgs,
		Long:    "Applies the locked preset and refuses unknown destinations on every reached sandbox. Session allowances pause; session denies and broader rules remain. Refused open flows close. The host keeps the restriction for later starts, including starts from another laptop. --off removes only this restriction and needs the literal word lockdown. It applies no local policy edits and fetches no credentials. Unreached hosts stay pending and make this command fail.",
		Example: "  " + brand.Name + " policy lockdown\n  " + brand.Name + " policy lockdown --host box\n  " + brand.Name + " policy lockdown --off --confirm=lockdown", RunE: func(cmd *cobra.Command, _ []string) error {
			if off && literal == "" {
				if !inputOn(cmd) {
					return &client.UserError{Msg: "lifting lockdown needs the literal word lockdown", Hint: prompts(cmd).withFlags("--off", "--confirm=lockdown"), Code: "lockdown_confirm", Kind: client.KindNeedsAnswer}
				}
				answer, err := prompts(cmd).ask("Type lockdown to lift the restriction: ")
				if err != nil {
					return err
				}
				literal = answer
			}
			if off && literal != "lockdown" || !off && literal != "" {
				return &client.UserError{Msg: "the lockdown confirmation does not match this action", Hint: "Use --off --confirm=lockdown to lift the restriction.", Code: "lockdown_confirm", Kind: client.KindNeedsAnswer}
			}
			hosts, err := client.LoadHosts()
			if err != nil {
				return err
			}
			if hostName != "" {
				found := false
				for _, host := range hosts {
					if host.Name == hostName {
						hosts = []client.Host{host}
						found = true
						break
					}
				}
				if !found {
					return &client.UserError{Msg: "no saved host named " + client.CleanLine(hostName), Hint: brand.Name + " host ls", Code: "host_not_found", Kind: client.KindNotFound}
				}
			}
			report, err := client.ChangeHostLockdownV1(cmd.Context(), hosts, !off, literal)
			if asJSON {
				if err != nil {
					return err
				}
				return printJSON(report)
			}
			verb := "enabled"
			if off {
				verb = "lifted"
			}
			for _, host := range report.Hosts {
				for _, session := range host.Result.Sessions {
					say(cmd, "%s/%s: lockdown %s.\n", client.CleanLine(host.Host), client.CleanLine(session.Session), verb)
				}
				if host.Error == "" && len(host.Result.Sessions) == 0 {
					say(cmd, "%s: lockdown %s for later sandbox starts.\n", client.CleanLine(host.Host), verb)
				}
			}
			if len(report.Unreached) > 0 {
				say(cmd, "Not reached: %s. The recorded change will be retried at the next contact.\n", client.CleanLine(strings.Join(report.Unreached, ", ")))
			}
			return err
		}}
	addHostFlag(cmd, &hostName, "change lockdown on this saved host only")
	cmd.Flags().BoolVar(&off, "off", false, "lift only the lockdown restriction")
	cmd.Flags().StringVar(&literal, "confirm", "", "the literal word lockdown, required with --off when input is off")
	addJSON(cmd, &asJSON)
	return cmd
}
