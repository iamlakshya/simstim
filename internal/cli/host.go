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
	"github.com/iamlakshya/simstim/internal/wall"
)

func hostCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "host",
		Short: "Add, create, list, test and remove hosts",
		Example: "  " + brand.Name + " host add my-vps\n" +
			"  " + brand.Name + " host ls",
	}
	cmd.AddCommand(hostAddCmd(), hostLsCmd(), hostShowCmd(), hostSetCmd(), hostUpgradeCmd(), hostRmCmd(), hostRekeyCmd(), hostTestCmd(), hostDoctorCmd(), hostRestoreCmd(), hostAlertsCmd(),
		hostCreateCmd(), hostDestroyCmd(), hostIdleCmd(),
		hostProviderCmd("start", "Start the host through its provider or provider script", ""),
		hostProviderCmd("stop", "Stop the host through its provider or provider script",
			"Stops the host through its provider or provider script. A VM's sessions stop too, and a sandbox's are paused."),
		hostProviderCmd("status", "Ask the provider or provider script whether the host is running", ""))
	return cmd
}

func hostAddCmd() *cobra.Command {
	var sshConfig, label, providerScript, hostKey, floor string
	var yes, wake, trusted bool
	var loc localMacOpts
	cmd := &cobra.Command{
		Use:   "add [<name>] [<ssh-target>]",
		Short: "Connect a host: check it, install the helper, prepare it",
		Long: "Connect a host, pin its SSH host key and install the helper. The ssh target is a Host\n" +
			"from your SSH config, or user@host. It defaults to the name. --ssh-config adds an extra\n" +
			"SSH config file, for example a repo's own. --host-key pins the host key with the given\n" +
			"SHA256 fingerprint instead of asking OpenSSH. --trusted marks the host as trusted (you\n" +
			"control root on it); hosts default to untrusted.\n\n" +
			"On a Mac, --local sets up this Mac as a host in a pinned Lima VM under your own account,\n" +
			"started automatically with limactl autostart.",
		Example: "  " + brand.Name + " host add my-vps\n" +
			"  " + brand.Name + " host add my-vps dev@203.0.113.10\n" +
			"  " + brand.Name + " host add my-vps dev@203.0.113.10 --host-key SHA256:… --trusted\n" +
			"  " + brand.Name + " host add --local",
		Args: cobra.RangeArgs(0, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			labels := parseLabels(label)
			if floor != "" {
				lvl, err := wall.ParseLevel(floor)
				if err != nil {
					return &client.UserError{Msg: fmt.Sprintf("--floor: %v", err), Hint: "Pass none, process, gvisor or microvm.", Code: "bad_value", Kind: client.KindUsage}
				}
				floor = lvl.String()
			}
			if loc.local {
				if sshConfig != "" || hostKey != "" || providerScript != "" || wake {
					return &client.UserError{
						Msg:  "--local cannot be combined with --ssh-config, --host-key, --provider-script or --wake",
						Hint: brand.Name + " host add --local",
						Code: "bad_flag",
						Kind: client.KindUsage,
					}
				}
				return addLocalMacHost(cmd, args, loc, labels, floor, trusted, yes)
			}
			for _, f := range []string{"cpus", "memory", "disk", "autostart", "idle-stop"} {
				if cmd.Flags().Changed(f) {
					return &client.UserError{
						Msg:  "--" + f + " needs --local",
						Hint: brand.Name + " host add --local --" + f + "=…",
						Code: "bad_flag",
						Kind: client.KindUsage,
					}
				}
			}
			if len(args) == 0 {
				return &client.UserError{
					Msg:  "host add needs a host name, or --local for this Mac",
					Hint: brand.Name + " host add my-vps (or " + brand.Name + " host add --local on a Mac)",
					Code: "missing_name",
					Kind: client.KindUsage,
				}
			}
			h := client.Host{Name: args[0], SSH: args[0], SSHConfig: sshConfig, Labels: labels, WallFloor: floor, Trusted: trusted}
			if providerScript != "" {
				h.Provider = &client.Provider{Script: providerScript, WakeOnAttach: wake}
			}
			if len(args) == 2 {
				h.SSH = args[1]
			}
			if !client.ValidHostName(h.Name) {
				return &client.UserError{Msg: fmt.Sprintf("%q cannot name a host", h.Name), Hint: "Use letters, digits, dots, dashes and underscores.", Code: "bad_name", Kind: client.KindUsage}
			}
			return addHostWithKey(cmd.Context(), prompts(cmd), h, hostKey, yes, yes)
		},
	}
	f := cmd.Flags()
	f.StringVar(&sshConfig, "ssh-config", "", "an extra SSH config file to include")
	f.StringVar(&hostKey, "host-key", "", "pin the host key with this SHA256 fingerprint (SHA256:…)")
	f.StringVar(&label, "label", "", "labels, for example provider=gcp,region=eu")
	f.BoolVarP(&yes, "yes", "y", false, "install the helper without asking, pin an unknown host key on first use, and offer no agents to install")
	f.StringVar(&providerScript, "provider-script", "", "a script that starts and stops the host (see examples/providers)")
	f.BoolVar(&wake, "wake", false, "start the host through its provider script when it does not answer")
	f.StringVar(&floor, "floor", "", "weakest sandbox level to pin: none, process, gvisor or microvm")
	f.BoolVar(&trusted, "trusted", false, "mark the host as trusted (you control root on it)")
	f.BoolVar(&loc.local, "local", false, "set up this Mac as a host in a pinned Lima VM")
	f.IntVar(&loc.cpus, "cpus", 0, "--local: VM CPUs (default: under half of this Mac)")
	f.IntVar(&loc.memoryGiB, "memory", 0, "--local: VM memory in GiB (default: under half of this Mac)")
	f.IntVar(&loc.diskGiB, "disk", 0, "--local: VM disk in GiB (sparse, default 40)")
	f.StringVar(&loc.autostart, "autostart", "", "--local: start VM automatically: boot, login or off (default boot)")
	f.StringVar(&loc.idleStop, "idle-stop", "", "--local: idle power-off time, or never (default never)")
	return cmd
}

func parseLabels(label string) map[string]string {
	if label == "" {
		return nil
	}
	out := map[string]string{}
	for _, kv := range strings.Split(label, ",") {
		k, v, _ := strings.Cut(kv, "=")
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out
}

// hostAddGuide changes the guide that host add offers agents with. Tests
// replace its sessions and its checks.
var hostAddGuide = func(*guide) {}

// addHost connects h and saves it. Its progress goes to stderr. Unless yes,
// it asks p before it installs the helper, and with input on it offers the
// agents that the host lacks, as setup does.
func addHost(ctx context.Context, p *prompter, h client.Host, yes bool) error {
	return addHostWithKey(ctx, p, h, "", yes, yes)
}

func addHostWithKey(ctx context.Context, p *prompter, h client.Host, hostKey string, installYes, skipAgents bool) error {
	consent := installConsent(ctx, p, h, installYes)
	pinOpts := client.PinOptions{
		HostKey:        hostKey,
		AcceptFirstUse: installYes || (h.Machine != nil && h.Machine.Provider == "lima"),
		Interactive:    p.on,
	}
	// connectHost records each change in p's log as it makes it, so a
	// cancel at any point after it says so.
	checks, err := connectHostWithPin(ctx, os.Stderr, p, h, pinOpts, consent, p.changes())
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
	if !skipAgents && p.asks(qAddAgents) {
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

// pinBeforeConnect pins the SSH host key for h before probing it, and reports
// whether a new pin file was written so connectHostWithPin can remove it on failure.
func pinBeforeConnect(ctx context.Context, pr *prompter, h client.Host, opts client.PinOptions, step func(string, ...any)) (bool, error) {
	if h.SSH == "" || (h.Machine != nil && h.Machine.Provider != "lima") {
		return false, nil
	}
	pin, err := client.PinHostKey(ctx, h, opts)
	if err != nil {
		var ue *client.UserError
		if pr != nil && errors.As(err, &ue) && ue.Kind == client.KindNeedsAnswer {
			ue.Hint = pr.withFlags("--host-key", "SHA256:…") + " (or " + pr.withFlags("--yes") + " to pin on first use)"
		}
		return false, err
	}
	switch pin.Source {
	case client.PinOnFirstUse:
		step("✓ host key %s pinned on first use. Check it on the machine: ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub", pin.Fingerprint)
	case client.PinExisting:
		step("✓ host key %s already pinned", pin.Fingerprint)
	default:
		step("✓ host key %s pinned (%s)", pin.Fingerprint, pin.Source)
	}
	return pin.Created, nil
}

// connectHostWithPin pins an SSH host's key, probes it, installs the helper
// after consent says yes, prepares the host, and saves it.
func connectHostWithPin(ctx context.Context, w io.Writer, pr *prompter, h client.Host, pinOpts client.PinOptions, consent func(q string) (bool, error), log *onboard.Log) ([]api.AgentCheck, error) {
	step := func(format string, a ...any) { fmt.Fprintf(w, "  "+format+"\n", a...) }
	fmt.Fprintf(w, "Connecting to %s (%s)…\n", h.Name, h.Target())
	start := time.Now()
	pctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	saved := false
	createdPin, err := pinBeforeConnect(pctx, pr, h, pinOpts, step)
	if err != nil {
		cancel()
		return nil, err
	}
	if createdPin {
		defer func() {
			if !saved {
				_ = client.RemovePin(ctx, h.Name)
			}
		}()
	}
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
		if p.DigestMismatch {
			step("✕ %s %s on %s does not match this client's digest; reinstalling", brand.HostBinary, client.HelperVersion(), h.Name)
		}
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
	if err := pinHostFloor(dctx, w, conn, &h); err != nil {
		return nil, err
	}
	checks, _ := conn.AgentChecks(dctx)
	// A cancel that came during the checks saves nothing (SaveHost).
	if err := client.SaveHost(ctx, h); err != nil {
		return nil, err
	}
	saved = true
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
