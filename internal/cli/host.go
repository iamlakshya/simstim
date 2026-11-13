package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/helpers"
	"github.com/iamlakshya/simstim/internal/onboard"
)

func hostCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "host",
		Short: "Add, create, list, test and remove hosts",
		Example: "  " + brand.Name + " host add my-vps\n" +
			"  " + brand.Name + " host ls",
	}
	cmd.AddCommand(hostAddCmd(), hostLsCmd(), hostRmCmd(), hostTestCmd(), hostDoctorCmd(), hostRestoreCmd(), hostAlertsCmd(),
		hostCreateCmd(), hostDestroyCmd(), hostIdleCmd(),
		hostProviderCmd("start", "Start the host through its provider or provider script", ""),
		hostProviderCmd("stop", "Stop the host through its provider or provider script",
			"Stops the host through its provider or provider script. A VM's sessions stop too, and a sandbox's are paused."),
		hostProviderCmd("status", "Ask the provider or provider script whether the host is running", ""))
	return cmd
}

func hostAddCmd() *cobra.Command {
	var sshConfig, providerScript string
	var labels []string
	var yes, wake bool
	cmd := &cobra.Command{
		Use:   "add <name> [<ssh-target>]",
		Short: "Connect a host: check it, install the helper, prepare it",
		Long: "Connect a host. The ssh target is a Host from your SSH config, or user@host.\n" +
			"It defaults to the name. --ssh-config adds an extra SSH config file, for example a repo's own.",
		Example: "  " + brand.Name + " host add my-vps" + "\n" +
			"  " + brand.Name + " host add my-vps dev@203.0.113.10",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			h := client.Host{Name: args[0], SSH: args[0], SSHConfig: sshConfig}
			if providerScript != "" {
				h.Provider = &client.Provider{Script: providerScript, WakeOnAttach: wake}
			}
			if len(args) == 2 {
				h.SSH = args[1]
			}
			if len(labels) > 0 {
				h.Labels = map[string]string{}
				for _, kv := range labels {
					k, v, _ := strings.Cut(kv, "=")
					h.Labels[strings.TrimSpace(k)] = strings.TrimSpace(v)
				}
			}
			if !client.ValidHostName(h.Name) {
				return &client.UserError{Msg: fmt.Sprintf("%q cannot name a host", h.Name), Hint: "Use letters, digits, dots, dashes and underscores.", Code: "bad_name", Kind: client.KindUsage}
			}
			return addHost(cmd.Context(), prompts(cmd), h, yes)
		},
	}
	cmd.Flags().StringVar(&sshConfig, "ssh-config", "", "an extra SSH config file to include")
	addLabel(cmd, &labels, "labels as key=value, for example provider=gcp,region=eu")
	addYes(cmd, &yes, "install the helper, and offer no agents to install")
	cmd.Flags().StringVar(&providerScript, "provider-script", "", "a script that starts and stops the host (see examples/providers)")
	cmd.Flags().BoolVar(&wake, "wake", false, "start the host through its provider script when it does not answer")
	return cmd
}

// hostAddGuide changes the guide that host add offers agents with. Tests
// replace its sessions and its checks.
var hostAddGuide = func(*guide) {}

// addHost connects h and saves it. Its progress goes to stderr. Unless yes,
// it asks p before it installs the helper, and with input on it offers the
// agents that the host lacks, as setup does.
func addHost(ctx context.Context, p *prompter, h client.Host, yes bool) error {
	consent := installConsent(ctx, p, h, yes)
	// connectHost records each change in p's log as it makes it, so a
	// cancel at any point after it says so.
	checks, err := connectHost(ctx, os.Stderr, h, consent, p.changes())
	if err != nil {
		if isCancel(err) || ctx.Err() != nil {
			return p.cancelOf(err)
		}
		return err
	}
	if checks != nil {
		fmt.Fprintln(os.Stderr, "\nAgents:")
		printAgentChecks(os.Stderr, h.Name, checks)
	}
	if !yes && p.asks(qAddAgents) {
		// The flow records each install in p's log as it makes it.
		f := onboard.NewFlow(h.Name, checks, p.changes())
		f.NoLogin = true
		if f.Missing() {
			g := newGuide(ctx, guideOpts{}, nil)
			g.in, g.out, g.p, g.tty, g.log = p.in, p.out, p, p.on, p.changes()
			hostAddGuide(g)
			if err := g.agents(h, f); err != nil {
				if isCancel(err) || errors.Is(err, context.Canceled) || ctx.Err() != nil {
					// The host is saved, maybe its helper and some agents
					// installed: the cancel says so (cli-spec 6.2, rule 6).
					return p.cancelOf(err)
				}
				return err
			}
		}
	}
	fmt.Fprintf(os.Stderr, "\n%s is ready. Start a session with `%s new %s --agent claude`, or run `%s`.\n", h.Name, brand.Name, h.Name, brand.Name)
	return nil
}

// installConsent is host add's question before it installs the helper on
// h: yes answers it. A cancel of ctx that came with the yes, after the
// question saw its answer, installs nothing.
func installConsent(ctx context.Context, p *prompter, h client.Host, yes bool) func(q string) (bool, error) {
	return func(q string) (bool, error) {
		if yes {
			return true, nil
		}
		ok, err := p.confirm(qInstallHelper, q, "a yes to install the helper on "+h.Name)
		switch {
		case err != nil:
			return false, err
		case !ok:
			return false, p.cancel()
		case stopped(ctx):
			// A signal that came with the yes: install nothing.
			return false, errInterrupted
		}
		return true, nil
	}
}

// connectHost checks a host, installs the helper after consent says yes,
// prepares the host, and saves it. It returns the host's agent checks, or
// none from a helper too old to check. log, when set, gets a line for each
// change.
func connectHost(ctx context.Context, w io.Writer, h client.Host, consent func(q string) (bool, error), log *onboard.Log) ([]api.AgentCheck, error) {
	step := func(format string, a ...any) { fmt.Fprintf(w, "  "+format+"\n", a...) }
	fmt.Fprintf(w, "Connecting to %s (%s)…\n", h.Name, h.Target())
	start := time.Now()
	pctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	s, err := client.HostTransport(pctx, h, true)
	if err != nil {
		cancel()
		return nil, err
	}
	p, err := client.Probe(pctx, s)
	cancel()
	if err != nil {
		return nil, err
	}
	step("✓ connected in %s: %s/%s", time.Since(start).Round(time.Millisecond), p.OS, p.Arch)
	if !p.Installed {
		bin, ok := helpers.For(p.OS, p.Arch)
		if !ok {
			return nil, &client.UserError{Msg: fmt.Sprintf("no helper for %s/%s in this build", p.OS, p.Arch), Hint: "Use a Linux host on amd64 or arm64.", Code: "no_helper"}
		}
		fmt.Fprintf(w, "\n%s needs a small helper on the host to keep sessions alive.\n", brand.DisplayName)
		fmt.Fprintf(w, "    What   %s %s, %.1f MB, SHA-256 checked\n", brand.HostBinary, client.HelperVersion(), float64(len(bin))/1e6)
		fmt.Fprintf(w, "    Where  ~/.local/share/%s/bin/%s/\n", brand.Name, client.HelperVersion())
		fmt.Fprintf(w, "    Also   keep sessions running after you log out, and install this\n")
		fmt.Fprintf(w, "           terminal's terminfo\n")
		fmt.Fprintf(w, "  Nothing listens on the network. To remove it later, delete ~/.local/share/%s on the host.\n\n", brand.Name)
		ok, err := consent("Install it?")
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, &client.UserError{Msg: "cancelled; nothing was installed", Hint: brand.Name + " host add " + h.Name, Code: "cancelled", Kind: client.KindCancelled}
		}
		ictx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		err = client.Install(ictx, s, p)
		cancel()
		if err != nil {
			return nil, err
		}
		step("✓ installed %s %s", brand.HostBinary, client.HelperVersion())
		log.Add("installed the helper on %s", h.Name)
	} else {
		step("✓ %s %s is already installed", brand.HostBinary, client.HelperVersion())
	}
	dctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	conn, err := client.Dial(dctx, h, true)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	res, err := conn.Setup(dctx)
	if err != nil {
		return nil, err
	}
	for _, s := range client.CleanChecks(res.Steps) {
		mark := "✓"
		if !s.OK {
			mark = "✕"
		}
		step("%s %s: %s", mark, s.Name, s.Detail)
		if !s.OK && s.Fix != "" {
			step("  Try: %s", s.Fix)
		}
	}
	d, err := conn.Doctor(dctx)
	if err != nil {
		return nil, err
	}
	step("✓ %s, %d CPUs, %.1f GiB, %.1f GiB of it for sessions", client.CleanLine(d.Distro), d.CPUs, float64(d.MemTotal)/(1<<30), float64(d.MemoryMax)/(1<<30))
	checks, _ := conn.AgentChecks(dctx)
	// A cancel that came during the checks saves nothing (SaveHost).
	if err := client.SaveHost(ctx, h); err != nil {
		return nil, err
	}
	log.Add("saved the host %s", h.Name)
	return client.CleanAgentChecks(checks), nil
}

func hostLsCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:     "ls",
		Short:   "List hosts",
		Example: "  " + brand.Name + " host ls\n  " + brand.Name + " host ls --json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			hosts, err := client.LoadHosts()
			if err != nil {
				return err
			}
			if asJSON {
				// Each host has its keys in snake_case, and in Go form until
				// the end of the old form.
				warnOld(cmd, oldHostKeys)
				objs := make([]map[string]any, len(hosts))
				for i, h := range hosts {
					objs[i] = hostObject(h)
				}
				return printJSON(objs)
			}
			if len(hosts) == 0 {
				say(cmd, "No hosts yet. Add one with `%s host add <name>`.\n", brand.Name)
				return nil
			}
			for _, h := range hosts {
				extra := ""
				if h.SSHConfig != "" {
					extra = "  (+ " + h.SSHConfig + ")"
				}
				fmt.Printf("%-16s %s%s\n", h.Name, h.Target(), extra)
			}
			return nil
		},
	}
	addJSON(cmd, &asJSON)
	return cmd
}

func hostRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "rm <host>",
		Short:   "Forget a host (its sessions keep running)",
		Example: "  " + brand.Name + " host rm my-vps",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			hosts, err := client.LoadHosts()
			if err != nil {
				return err
			}
			names := make([]string, 0, len(hosts))
			for _, h := range hosts {
				names = append(names, h.Name)
			}
			out := hosts[:0]
			var gone *client.Host
			for _, h := range hosts {
				if h.Name == args[0] {
					gone = &h
					continue
				}
				out = append(out, h)
			}
			if gone == nil {
				return &client.UnknownNameError{
					UserError: &client.UserError{Msg: fmt.Sprintf("no host named %q", args[0]), Hint: brand.Name + " host ls", Code: "host_not_found", Kind: client.KindNotFound},
					Name:      args[0], Known: names,
				}
			}
			if err := client.SaveHosts(cmd.Context(), out); err != nil {
				return err
			}
			say(cmd, "Forgot %s. Its sessions and the helper stay on the host.\n", args[0])
			if m := gone.Machine; m != nil {
				say(cmd, "Its %s machine %s is kept. `host destroy` would have deleted it; now delete it with %s's own tools.\n", m.Provider, m.ID, m.Provider)
			}
			return nil
		},
	}
}
