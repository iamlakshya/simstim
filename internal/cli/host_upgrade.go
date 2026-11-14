package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// host upgrade (docs/contributing/cli-spec.md 11.4) installs this
// client's helper on a host, without the other steps of host add.
// Isolation task P4.1 adds the check of the helper's digest to it.

// The outcomes of host upgrade, as its --json prints them.
const (
	upgradeUnchanged = "unchanged"
	upgradeInstalled = "installed"
)

// upgradeResult is what host upgrade did (cli-spec 7.1).
type upgradeResult struct {
	Host    string `json:"host"`
	Helper  string `json:"helper"`
	Outcome string `json:"outcome"`
}

func hostUpgradeCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "upgrade <host>",
		Short: "Install this client's helper on a host",
		Long: "Install this client's helper on a saved host when it is missing. It reads the host without\n" +
			"starting the helper, installs the helper, then connects. It asks nothing: the command is\n" +
			"your yes. It removes no helper, so a newer client's helper stays. Running sessions keep\n" +
			"their own helper, and new sessions use this one.",
		Example: "  " + brand.Name + " host upgrade my-vps\n  " + brand.Name + " host upgrade my-vps --json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			h, err := client.FindHost(args[0])
			if err != nil {
				return err
			}
			res, err := upgradeHost(cmd.Context(), h)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(res)
			}
			if res.Outcome == upgradeUnchanged {
				say(cmd, "%s has helper %s already.\n", h.Name, res.Helper)
				return nil
			}
			say(cmd, "Installed %s %s on %s.\n", brand.HostBinary, res.Helper, h.Name)
			return nil
		},
	}
	addJSON(cmd, &asJSON)
	return cmd
}

// upgradeHost installs this client's helper on h when it is missing,
// without starting the bridge first (cli-spec 11.4), then connects. It
// never prunes: an older client must not remove a newer client's helper.
func upgradeHost(ctx context.Context, h client.Host) (upgradeResult, error) {
	res := upgradeResult{Host: h.Name, Outcome: upgradeUnchanged}
	pctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	s, err := client.HostTransport(pctx, h, true)
	if err != nil {
		cancel()
		return res, err
	}
	p, err := client.Probe(pctx, s)
	cancel()
	if err != nil {
		return res, err
	}
	// Probe loads the helpers of a client built with go install, and
	// their version is part of the helper's.
	res.Helper = client.HelperVersion()
	if p.Installed {
		return res, nil
	}
	ictx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	err = client.InstallHelper(ictx, s, p)
	cancel()
	if err != nil {
		return res, installError(h.Name, err)
	}
	// The check dial installs nothing and prunes nothing: when the helper
	// is not there after all, it fails, and says so.
	dctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	conn, err := client.Dial(client.WithoutInstall(dctx), h, true)
	if err != nil {
		return res, &client.UserError{Msg: fmt.Sprintf("installed the helper %s on %s, but it did not start: %s", res.Helper, h.Name, err.Error()),
			Hint: brand.Name + " host doctor " + h.Name, Code: "helper_failed"}
	}
	conn.Close()
	res.Outcome = upgradeInstalled
	return res, nil
}

// installError gives an install that failed on the host, such as a full
// disk or a folder that cannot be written, its host and a hint. An error
// that has its own hint already stays as it is.
func installError(host string, err error) error {
	var ue *client.UserError
	if errors.As(err, &ue) {
		return err
	}
	return &client.UserError{
		Msg:  fmt.Sprintf("could not install the helper on %s: %v", host, err),
		Hint: fmt.Sprintf("Check the free disk space and the permissions of ~/.local/share/%s/bin on %s, then run the command again.", brand.Name, host),
		Code: "install_failed",
	}
}
