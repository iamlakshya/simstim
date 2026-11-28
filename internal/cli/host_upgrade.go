package cli

import (
	"context"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

type hostUpgradeResult struct {
	Host    string `json:"host"`
	Helper  string `json:"helper"`
	Outcome string `json:"outcome"`
}

func hostUpgradeCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "upgrade <host>",
		Short: "Install or update this client's helper on a host",
		Long: "Checks the helper on a host without starting the bridge first, and installs or\n" +
			"reinstalls this client's matching helper when it is missing or its SHA-256 digest\n" +
			"does not match. Newer helpers from other clients stay, and running sessions keep\n" +
			"their own process.",
		Example: "  " + brand.Name + " host upgrade my-vps\n  " + brand.Name + " host upgrade my-vps --json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHostUpgrade(cmd.Context(), cmd, args[0], asJSON)
		},
	}
	addJSON(cmd, &asJSON)
	return cmd
}

func runHostUpgrade(ctx context.Context, cmd *cobra.Command, name string, asJSON bool) error {
	h, err := client.FindHost(name)
	if err != nil {
		return err
	}
	interactive := !inputOff(cmd)
	pctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	s, err := client.HostTransport(pctx, h, interactive)
	if err != nil {
		cancel()
		return err
	}
	p, err := client.Probe(pctx, s)
	cancel()
	if err != nil {
		return err
	}
	ver := client.HelperVersion()
	if p.Installed {
		say(cmd, "%s has helper %s already.\n", h.Name, ver)
		if asJSON {
			return printJSON(hostUpgradeResult{Host: h.Name, Helper: ver, Outcome: "unchanged"})
		}
		return nil
	}

	outcome := "installed"
	if p.DigestMismatch {
		outcome = "reinstalled"
	}
	ictx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	err = client.InstallNoPrune(ictx, s, p)
	cancel()
	if err != nil {
		return err
	}

	// Verify the installed helper starts cleanly and passes the digest check.
	dctx, cancel := context.WithTimeout(client.WithoutInstall(ctx), 60*time.Second)
	conn, err := client.Dial(dctx, h, interactive)
	cancel()
	if err != nil {
		return err
	}
	conn.Close()

	if outcome == "reinstalled" {
		say(cmd, "Reinstalled helper %s on %s.\n", ver, h.Name)
	} else {
		say(cmd, "Installed helper %s on %s.\n", ver, h.Name)
	}
	if asJSON {
		return printJSON(hostUpgradeResult{Host: h.Name, Helper: ver, Outcome: outcome})
	}
	return nil
}
