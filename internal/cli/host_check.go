package cli

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/provider/lima"
)

// The host commands that check a host or report on it: test, doctor,
// restore, and the provider's start, stop and status.

func hostTestCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:     "test <name>",
		Short:   "Check that a host answers, and how fast",
		Example: "  " + brand.Name + " host test my-vps\n  " + brand.Name + " host test my-vps --json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			h, err := client.FindHost(args[0])
			if err != nil {
				return err
			}
			start := time.Now()
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			conn, err := client.Dial(ctx, h, true)
			if err != nil {
				return err
			}
			defer conn.Close()
			connect := time.Since(start)
			rt, err := conn.RoundTrip(ctx)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(map[string]any{"host": h.Name, "connect_ms": connect.Milliseconds(), "round_trip_ms": rt.Milliseconds()})
			}
			printf("%s answers: connect %s, round trip %s\n", h.Name, connect.Round(time.Millisecond), rt.Round(time.Millisecond))
			return nil
		},
	}
	addJSON(cmd, &asJSON)
	return cmd
}

func hostDoctorCmd() *cobra.Command {
	var asJSON, fix, yes bool
	cmd := &cobra.Command{
		Use:   "doctor <name>",
		Short: "Check a host's setup, and with --fix fix what it can",
		Long: "Checks a host: systemd and linger, the restore unit, terminfo, memory caps, swap, the\n" +
			"session sockets and the agents. It warns when the host has less memory than an agent's\n" +
			"docs ask for, and when a program in one session could drive another through its\n" +
			"socket. For a VM made by host create lima, it also checks, on this computer, that the\n" +
			"VM cannot reach services on this computer's loopback address. With --fix it offers to\n" +
			"fix linger, the restore unit, Ghostty's terminfo, a broken current helper link, stale\n" +
			"session sockets and old helper versions. Each fix is shown and explained before it\n" +
			"runs; one that needs root is printed for you.",
		Example: "  " + brand.Name + " host doctor my-vps\n  " + brand.Name + " host doctor my-vps --fix\n  " + brand.Name + " host doctor my-vps --json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if asJSON && fix && !yes {
				// With --json nothing is asked, so -y must answer for each fix.
				return prompts(cmd).needs(qHostFix, "a yes for each fix")
			}
			h, err := client.FindHost(args[0])
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()
			conn, err := client.Dial(ctx, h, true)
			if err != nil {
				return err
			}
			defer conn.Close()
			d, err := conn.Doctor(ctx)
			if err != nil {
				return err
			}
			if h.Machine != nil {
				if check := machineChecks[h.Machine.Provider]; check != nil {
					d.Checks = append(d.Checks, check(ctx, h))
				}
			}
			if d.AgentChecks, err = conn.AgentChecks(ctx); err != nil {
				return err
			}
			if asJSON && fix {
				return hostFixesJSON(ctx, conn, h, d)
			}
			if asJSON {
				return printJSON(d)
			}
			d = client.CleanHostInfo(d) // only the human view is cleaned
			printf("%s: %s (%s/%s), %d CPUs, %.1f GiB, %.1f GiB free disk, helper %s\n", h.Name, d.Distro, d.OS, d.Arch, d.CPUs,
				float64(d.MemTotal)/(1<<30), float64(d.DiskFree)/(1<<30), d.Version)
			printChecks(os.Stdout, os.Stderr, d.Checks)
			printf("Agents:\n")
			printAgentChecks(os.Stdout, h.Name, d.AgentChecks)
			printScrollback(ctx, conn)
			if !fix {
				return nil
			}
			return fixHost(cmd, conn, h, yes)
		},
	}
	addJSON(cmd, &asJSON)
	cmd.Flags().BoolVar(&fix, "fix", false, "offer to fix the problems found, one by one")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "with --fix, apply every fix without asking")
	return cmd
}

// hostFixesJSON applies every fix of h, as host doctor --fix -y --json
// does, and prints the doctor's report with the fixes as they ended.
func hostFixesJSON(ctx context.Context, conn *client.Conn, h client.Host, d client.HostInfo) error {
	if stopped(ctx) {
		return errInterrupted
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	fixes, err := conn.HostFixes(cctx, false, nil)
	if err != nil {
		return err
	}
	// A cancel that comes while the host lists its fixes sends no apply:
	// no request goes after a cancel (Conn.Call).
	res, err := applyAll(fixes, func(ids []string) ([]api.Fix, error) {
		return conn.HostFixes(cctx, true, ids)
	})
	if err != nil {
		return err
	}
	for i := range res {
		// A command to run by hand names the host as the user reaches it.
		res[i].Command = strings.ReplaceAll(res[i].Command, "<host>", h.SSH)
	}
	return printJSON(struct {
		client.HostInfo
		Fixes []api.Fix `json:"fixes"`
	}{d, res})
}

func hostRestoreCmd() *cobra.Command {
	var all, asJSON bool
	cmd := &cobra.Command{
		Use:     "restore <name>",
		Short:   "Start again the sessions a host restart stopped (the host also does this at boot)",
		Example: "  " + brand.Name + " host restore my-vps\n  " + brand.Name + " host restore my-vps --all --json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			h, err := client.FindHost(args[0])
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()
			conn, err := client.Dial(ctx, h, true)
			if err != nil {
				return err
			}
			defer conn.Close()
			res, err := conn.Restore(ctx, all)
			if err != nil {
				return err
			}
			if asJSON {
				if res == nil {
					res = []api.RestoreResult{}
				}
				return printJSON(map[string]any{"host": h.Name, "results": res})
			}
			if len(res) == 0 {
				say(cmd, "Nothing to restore on %s.\n", h.Name)
			}
			printRestore(os.Stdout, res)
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "also restart sessions whose holder crashed without a host restart")
	addJSON(cmd, &asJSON)
	return cmd
}

func hostProviderCmd(action, short string) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:     action + " <name>",
		Short:   short,
		Example: "  " + brand.Name + " host " + action + " my-vps",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			h, err := client.FindHost(args[0])
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Minute)
			defer cancel()
			if asJSON {
				return hostStatusJSON(ctx, h)
			}
			if h.Machine != nil {
				return machineAction(ctx, h, action)
			}
			out, err := client.RunProvider(ctx, h, action)
			if err != nil {
				return err
			}
			if s := strings.TrimSpace(string(out)); s != "" {
				printf("%s\n", s)
			} else {
				say(cmd, "%s: %s done.\n", h.Name, action)
			}
			return nil
		},
	}
	if action == "status" {
		cmd.Example = "  " + brand.Name + " host status my-vps\n  " + brand.Name + " host status my-vps --json"
		addJSON(cmd, &asJSON)
	}
	return cmd
}

// hostStatusJSON prints whether h runs, for scripts: the state of a machine
// that `host create` made, or what the provider script printed.
func hostStatusJSON(ctx context.Context, h client.Host) error {
	if h.Machine != nil {
		lc, err := client.OpenMachine(h)
		if err != nil {
			return err
		}
		st, err := lc.Status(ctx)
		if err != nil {
			return err
		}
		return printJSON(map[string]string{"host": h.Name, "state": st, "provider": h.Machine.Provider, "id": h.Machine.ID})
	}
	out, err := client.RunProvider(ctx, h, "status")
	if err != nil {
		return err
	}
	return printJSON(map[string]string{"host": h.Name, "output": strings.TrimSpace(string(out))})
}

// machineChecks run provider-specific security checks on this computer.
var machineChecks = map[string]func(context.Context, client.Host) api.Check{
	lima.Provider{}.Name(): lima.LoopbackCheck,
}
