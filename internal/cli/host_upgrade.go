package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

type hostUpgradeResult struct {
	Host         string                     `json:"host"`
	Helper       string                     `json:"helper"`
	Outcome      string                     `json:"outcome"`
	RuntimeLabel string                     `json:"runtime_label,omitempty"`
	Runtime      []api.RuntimeInstalledFile `json:"runtime,omitempty"`
}

func hostUpgradeCmd() *cobra.Command {
	var asJSON bool
	var imagesMode string
	cmd := &cobra.Command{
		Use:   "upgrade <host>",
		Short: "Install or update this client's helper and runtime files on a host",
		Long: "Checks the helper on a host without starting the bridge first, and installs or\n" +
			"reinstalls this client's matching helper when it is missing or its SHA-256 digest\n" +
			"does not match. Newer helpers from other clients stay, and running sessions keep\n" +
			"their own process. When this release names sandbox runtime files that the host\n" +
			"lacks, host upgrade installs them too.",
		Example: "  " + brand.Name + " host upgrade my-vps\n  " + brand.Name + " host upgrade my-vps --images laptop\n  " + brand.Name + " host upgrade my-vps --json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if imagesMode != "" {
				parsed, err := api.ParseImagesFetch(imagesMode)
				if err != nil {
					return &client.UserError{Msg: fmt.Sprintf("--images: %v", err), Hint: "Pass auto, host or laptop.", Code: "bad_value", Kind: client.KindUsage}
				}
				imagesMode = parsed
			}
			return runHostUpgrade(cmd.Context(), cmd, args[0], imagesMode, asJSON)
		},
	}
	addImages(cmd, &imagesMode, "how runtime files reach the host: auto, host or laptop")
	addJSON(cmd, &asJSON)
	return cmd
}

func runHostUpgrade(ctx context.Context, cmd *cobra.Command, name, imagesMode string, asJSON bool) error {
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
	manifest := runtimeManifestFunc()
	hasRuntime := len(manifest.ItemsForArch(p.Arch)) > 0

	outcome := "unchanged"
	if !p.Installed {
		outcome = "installed"
		if p.DigestMismatch {
			outcome = "reinstalled"
		}
		ictx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		err = client.InstallNoPrune(ictx, s, p)
		cancel()
		if err != nil {
			return err
		}
	}

	var rtRes client.EnsureRuntimeResult
	if !p.Installed || hasRuntime {
		// Verify the installed helper starts cleanly and passes the digest check,
		// and install any missing runtime files named by the release manifest.
		dctx, cancel := context.WithTimeout(client.WithoutInstall(ctx), 60*time.Second)
		conn, err := client.Dial(dctx, h, interactive)
		cancel()
		if err != nil {
			return err
		}
		defer conn.Close()
		if hasRuntime {
			var progressOut io.Writer
			if !asJSON {
				progressOut = os.Stderr
			}
			rctx, rcancel := context.WithTimeout(ctx, 10*time.Minute)
			rtRes, err = client.EnsureRuntime(rctx, conn, client.EnsureRuntimeOptions{
				Mode:        imagesMode,
				Manifest:    manifest,
				Arch:        p.Arch,
				Transport:   s,
				Interactive: interactive,
				Out:         progressOut,
			})
			rcancel()
			if err != nil {
				return err
			}
		}
	}

	switch outcome {
	case "unchanged":
		if n := len(rtRes.Installed); n == 1 {
			say(cmd, "%s has helper %s already; installed 1 runtime file.\n", h.Name, ver)
		} else if n > 1 {
			say(cmd, "%s has helper %s already; installed %d runtime files.\n", h.Name, ver, n)
		} else {
			say(cmd, "%s has helper %s already.\n", h.Name, ver)
		}
	case "reinstalled":
		say(cmd, "Reinstalled helper %s on %s.\n", ver, h.Name)
	default:
		say(cmd, "Installed helper %s on %s.\n", ver, h.Name)
	}
	if asJSON {
		out := hostUpgradeResult{
			Host:    h.Name,
			Helper:  ver,
			Outcome: outcome,
		}
		if len(rtRes.Installed) > 0 {
			out.RuntimeLabel = rtRes.Runtime
			out.Runtime = rtRes.Installed
		}
		return printJSON(out)
	}
	return nil
}
