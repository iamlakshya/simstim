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
		Use:     "test <host>",
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
		Use:   "doctor <host>",
		Short: "Check a host's setup, and with --fix fix what it can",
		Long: "Checks systemd, terminfo, memory caps, swap and the agents on a host.\n" +
			"It checks that sessions keep running after you log out. It also checks the start-up entry\n" +
			"that brings sessions back after a reboot. It warns when the host has less memory than\n" +
			"an agent's docs ask for. It warns when a program in one session could drive another\n" +
			"through its socket. For a VM made by host create lima, it checks, on this computer, that\n" +
			"the VM cannot reach services on this computer's loopback address.\n\n" +
			"With --fix it offers to fix what it found: sessions that stop when you log out, the start-up\n" +
			"entry and Ghostty's terminfo. It also fixes a broken current helper link, stale session sockets\n" +
			"and old helper versions. Each fix is shown and explained before it runs. A fix that needs root\n" +
			"is printed for you.",
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
	addFix(cmd, &fix)
	addYes(cmd, &yes, "apply each fix that --fix offers")
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
		Use:     "restore <host>",
		Short:   "Start again the sessions that a host restart stopped",
		Long:    "Starts again the sessions that a host restart stopped. The host also does this when it starts.",
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
	addAll(cmd, &all, "also restart sessions that crashed without a host restart")
	addJSON(cmd, &asJSON)
	return cmd
}

func hostProviderCmd(action, short, long string) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:     action + " <host>",
		Short:   short,
		Long:    long,
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
