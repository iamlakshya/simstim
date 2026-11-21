package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/onboard"
)

func hostCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "host",
		Short: "Add, create, list, test and remove hosts",
		Example: "  " + brand.Name + " host add my-vps\n" +
			"  " + brand.Name + " host ls",
	}
	cmd.AddCommand(hostAddCmd(), hostLsCmd(), hostShowCmd(), hostSetCmd(), hostRmCmd(), hostRekeyCmd(), hostTestCmd(), hostDoctorCmd(), hostUpgradeCmd(), hostRestoreCmd(), hostAlertsCmd(),
		hostCreateCmd(), hostDestroyCmd(), hostIdleCmd(), hostDefaultCmd(),
		hostProviderCmd("start", "Start the host through its provider or provider script", ""),
		hostProviderCmd("stop", "Stop the host through its provider or provider script",
			"Stops the host through its provider or provider script. A VM's sessions stop too, and a sandbox's are paused."),
		hostProviderCmd("status", "Ask the provider or provider script whether the host is running", ""), hostPrepareCmd())
	cmd.AddCommand(hostInviteCmd(), hostJoinCmd(), hostLeaveCmd(), hostJoinApplyCmd(), hostJoinProbeCmd(), hostJoinLeaveCmd())
	cmd.AddCommand(localMachineExtraCommandsV1()...)
	return cmd
}

func hostAddCmd() *cobra.Command {
	var sshConfig, providerScript, tierStr, accept, address string
	var hostKeys []string
	var acceptFlags joinedAcceptFlagsV1
	var rawRootMode, imagesMode string
	var labels []string
	var yes, wake, noSandbox, delegateIO, separateAccounts bool
	cmd := &cobra.Command{
		Use:   "add <name> [<ssh-target>]",
		Short: "Connect a host: check it, install the helper, prepare it",
		Long: "Connect a host, prepare its sandbox runtime and test its wall before saving\n" +
			"its wall floor. The ssh target is a Host from your SSH config, or user@host;\n" +
			"it defaults to the name. --no-sandbox skips sandbox preparation. --ssh-config\n" +
			"adds an extra SSH config file, for example a repo's own.",
		Example: "  " + brand.Name + " host add my-vps\n" +
			"  " + brand.Name + " host add my-vps dev@203.0.113.10\n" +
			"  " + brand.Name + " host add my-vps --tier own --yes\n" +
			"  " + brand.Name + " host add my-vps --no-sandbox",
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
				return &client.UserError{Msg: fmt.Sprintf("%q cannot name a host", h.Name), Hint: "Use letters, digits, dots, dashes and underscores, with no dot at the end.", Code: "bad_name", Kind: client.KindUsage}
			}
			var tier client.Tier
			if tierStr != "" {
				var err error
				if tier, err = client.ParseTier(tierStr); err != nil {
					return err
				}
			}
			if accept != "" {
				if len(args) != 1 || providerScript != "" || len(labels) > 0 || wake {
					return &client.UserError{Msg: "--accept uses only its original invite and checked reply", Hint: "Use host add <name> --accept - --host-key SHA256:… --tier <tier>.", Code: "bad_value", Kind: client.KindUsage}
				}
				options, err := parseInviteAcceptOptionsV1(cmd, yes)
				if err != nil {
					return err
				}
				options.sshConfig = sshConfig
				return acceptJoinedHost(cmd, args[0], accept, address, hostKeys, tier, yes, options)
			}
			if address != "" || len(hostKeys) > 1 {
				return &client.UserError{Msg: "multiple host keys and --address need --accept -", Hint: "Use one --host-key for ordinary host add.", Code: "bad_value", Kind: client.KindUsage}
			}
			hostKey := ""
			if len(hostKeys) == 1 {
				hostKey = hostKeys[0]
			}
			return addHostPrepared(cmd, prompts(cmd), h, hostAddOptions{
				yes:              yes,
				wantKey:          hostKey,
				tier:             tier,
				needTier:         true,
				noSandbox:        noSandbox,
				rawRootMode:      rawRootMode,
				imagesMode:       imagesMode,
				delegateIO:       delegateIO,
				separateAccounts: separateAccounts,
				wake:             wake,
			})
		},
	}
	cmd.Flags().StringVar(&sshConfig, "ssh-config", "", "an extra SSH config file to include")
	addLabel(cmd, &labels, "labels as key=value, for example provider=gcp,region=eu")
	addYes(cmd, &yes, "install the helper, pin its first host key, and offer no agents to install (needs --tier)")
	cmd.Flags().StringSliceVar(&hostKeys, "host-key", nil, "the SHA256 `fingerprint` of the host's key, as ssh-keygen -lf prints it on the host: the key to pin")
	cmd.Flags().StringVar(&accept, "accept", "", "read the reply from standard input, with --accept -")
	cmd.Flags().StringVar(&address, "address", "", "select an address from the reply")
	cmd.Flags().StringVar(&tierStr, "tier", "", "who else has root on the host: own, throwaway or shared")
	cmd.Flags().BoolVar(&noSandbox, "no-sandbox", false, "skip sandbox preparation and keep the host without a wall floor")
	addImages(cmd, &imagesMode, "get runtime and image bytes from auto, host or laptop (default: [images] fetch)")
	addRootStep(cmd, &rawRootMode, "handle optional root changes: run, print or skip (default depends on tier and sandbox history)")
	cmd.Flags().BoolVar(&delegateIO, "delegate-io", false, "offer disk I/O controller delegation for this user")
	cmd.Flags().BoolVar(&separateAccounts, "separate-accounts", false, "offer the signed launcher and separate sandbox accounts")
	cmd.Flags().StringVar(&providerScript, "provider-script", "", "a script that starts and stops the host (see examples/providers)")
	cmd.Flags().BoolVar(&wake, "wake", false, "start the host through its provider script when it does not answer")
	addInviteAcceptFlagsV1(cmd, &acceptFlags)
	return cmd
}

// hostAddGuide changes the guide that host add offers agents with. Tests
// replace its sessions and its checks.
var hostAddGuide = func(*guide) {}

// beforeSetupSave runs just before connectHost saves the host. Tests run
// another command there.
var beforeSetupSave = func(client.Host) {}

// addHost connects h and saves it. Its progress goes to stderr. Unless yes,
// it asks p before it installs the helper, and with input on it offers the
// agents that the host lacks, as setup does.
func addHost(ctx context.Context, p *prompter, h client.Host, yes bool) error {
	return addHostKey(ctx, p, h, yes, "")
}

// addHostKey is addHost, with want, the fingerprint that --host-key gives.
// It pins h's host key at the first connection (isolation spec 14.7.1).
func addHostKey(ctx context.Context, p *prompter, h client.Host, yes bool, want string) error {
	var tier client.Tier
	if h.Machine != nil {
		tier = client.ProviderTier(h.Machine.Provider)
	}
	return addHostWithTier(ctx, p, h, yes, want, tier, h.Machine == nil)
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

// hostKeyOptions says how host add and setup pin h's host key: as want
// says, or as the user's own SSH settings trust it, and otherwise on first
// use with yes, after ssh's question at a terminal, or not at all. A saved
// host keeps its pin, whatever its target: only host rekey changes it. A
// host that is not saved yet gets a new pin, whatever is left in its file.
func hostKeyOptions(h client.Host, want string, yes, terminal bool) *client.PinOptions {
	o := &client.PinOptions{Want: want, FirstUse: client.FirstUseRefuse, Replace: true}
	switch {
	case yes:
		o.FirstUse = client.FirstUseAccept
	case terminal:
		o.FirstUse = client.FirstUseAsk
	}
	if _, err := client.FindHost(h.Name); err == nil {
		o.Replace = false
	}
	return o
}

// connectHost checks a host, installs the helper after consent says yes,
// prepares the host, and saves it. It returns the host's agent checks, or
// none from a helper too old to check. log, when set, gets a line for each
// change. A host reached over SSH gets its host key pinned first, as pin
// says. The new pin stays apart until the host is saved, so a host add that
// fails leaves no pin.
func connectHost(ctx context.Context, w io.Writer, save client.HostChange, chooseTier func() (client.Tier, error), consent func(q string) (bool, error), log *onboard.Log, pin *client.PinOptions) ([]api.AgentCheck, error) {
	checks, _, err := connectHostCore(ctx, w, save, chooseTier, consent, log, pin, false)
	return checks, err
}

func connectHostForPreparation(ctx context.Context, w io.Writer, save client.HostChange, chooseTier func() (client.Tier, error), consent func(q string) (bool, error), log *onboard.Log, pin *client.PinOptions) ([]api.AgentCheck, *client.HostPrepareTargetV1, error) {
	return connectHostCore(ctx, w, save, chooseTier, consent, log, pin, true)
}

func connectHostCore(ctx context.Context, w io.Writer, save client.HostChange, chooseTier func() (client.Tier, error), consent func(q string) (bool, error), log *onboard.Log, pin *client.PinOptions, forPrepare bool) ([]api.AgentCheck, *client.HostPrepareTargetV1, error) {
	ctx = client.WithTierQuestion(ctx, nil) // this flow owns its add question
	h := *save.Now
	save.Pin = true
	ctx, abort, err := client.StagePinOwned(ctx, h.Name)
	if err != nil {
		return nil, nil, err
	}
	defer abort()
	observation, err := client.ObserveHostRegistrationContext(ctx, save)
	if err != nil {
		return nil, nil, err
	}
	ctx = client.WithHostStateObservation(ctx, observation)
	step := func(format string, a ...any) { fmt.Fprintf(w, "  "+format+"\n", a...) }
	fmt.Fprintf(w, "Connecting to %s (%s)…\n", h.Name, h.Target())
	start := time.Now()
	limit := 60 * time.Second
	if pin.FirstUse == client.FirstUseAsk {
		limit += 5 * time.Minute // ssh may ask about the host key
	}
	var pinned client.Pin
	pin.Pinned = &pinned
	pctx, cancel := context.WithTimeout(client.WithPin(ctx, pin), limit)
	s, baseline, err := client.PrepareHostRegistrationBaselineV1(pctx, h, true)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	p, err := client.Probe(pctx, s)
	cancel()
	if err != nil {
		return nil, nil, err
	}
	step("✓ connected in %s: %s/%s", time.Since(start).Round(time.Millisecond), p.OS, p.Arch)
	if pinned.Source != "" {
		step("✓ %s", pinLine(h, pinned))
	}
	if chooseTier != nil {
		t, err := chooseTier()
		if err != nil {
			return nil, nil, err
		}
		save.Tier = t
	}
	if err := ensureHostHelper(ctx, w, h.Name, s, p, consent, log); err != nil {
		return nil, nil, err
	}
	dctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	conn, err := client.Dial(dctx, h, true)
	if err != nil {
		return nil, nil, err
	}
	defer conn.Close()
	if conn.HostIDChanged() {
		// noteHostID downgraded a saved host whose host ID changed: do not
		// silently raise it here without host set's typed confirmation.
		save.Tier = client.TierShared
	}
	save.HostID = conn.HostID()
	if err := conn.RecordHostBaseline(ctx, baseline); err != nil {
		return nil, nil, err
	}
	res, err := conn.Setup(dctx)
	if err != nil {
		return nil, nil, err
	}
	for _, s := range client.CleanChecks(res.Steps) {
		mark := "✓"
		if !s.OK {
			mark = "✕"
		}
		step("%s %s: %s", mark, shown(s.Name), shown(s.Detail))
		if !s.OK && s.Fix != "" {
			step("  Try: %s", shown(s.Fix))
		}
	}
	d, err := conn.Doctor(dctx)
	if err != nil {
		return nil, nil, err
	}
	step("✓ %s, %d CPUs, %.1f GiB, %.1f GiB of it for sessions", client.CleanLine(d.Distro), d.CPUs, float64(d.MemTotal)/(1<<30), float64(d.MemoryMax)/(1<<30))
	checks, _ := conn.AgentChecks(dctx)
	var target *client.HostPrepareTargetV1
	if forPrepare {
		target, err = saveAddedForPreparation(ctx, save, pinned, log)
	} else {
		err = saveAdded(ctx, save, pinned, log)
	}
	if err != nil {
		return nil, nil, err
	}
	return client.CleanAgentChecks(checks), target, nil
}

// pinLine is the line of host add about h's pin.
func pinLine(h client.Host, pinned client.Pin) string {
	line := pinned.Line()
	if pinned.Source == client.PinFirstUse && h.Machine == nil {
		line += ". Check it on the machine: " + pinned.CheckCommand()
	}
	return line
}

// saveAdded saves h, and the new pin that host add staged for it, together,
// under the pin's lock, and records it in log.
func saveAdded(ctx context.Context, save client.HostChange, pinned client.Pin, log *onboard.Log) error {
	h := *save.Now
	beforeSetupSave(h)
	if err := client.UpdateHosts(ctx, save); err != nil {
		return err
	}
	recordSavedHostLog(h.Name, pinned, log)
	return nil
}

func saveAddedForPreparation(ctx context.Context, save client.HostChange, pinned client.Pin, log *onboard.Log) (*client.HostPrepareTargetV1, error) {
	h := *save.Now
	beforeSetupSave(h)
	target, err := client.UpdateHostsForPreparation(ctx, save)
	if err != nil {
		return nil, err
	}
	recordSavedHostLog(h.Name, pinned, log)
	return target, nil
}

func recordSavedHostLog(name string, pinned client.Pin, log *onboard.Log) {
	if pinned.Source != "" && pinned.Source != client.PinKept {
		log.Add("saved the host %s, with its pinned host key", name)
	} else {
		log.Add("saved the host %s", name)
	}
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
			tiers, err := client.LoadTiersContext(cmd.Context(), hosts)
			if err != nil {
				return err
			}
			health := readHostsHealthV1(cmd.Context(), hosts)
			if err := cmd.Context().Err(); err != nil {
				return err
			}
			if asJSON {
				// Each host has its keys in snake_case, and in Go form until
				// the end of the old form.
				warnOld(cmd, oldHostKeys)
				objs := make([]map[string]any, len(hosts))
				for i, h := range hosts {
					objs[i] = hostObject(h, tiers.Of(h.Name))
					if facts, ok := health[h.Name]; ok {
						objs[i]["health"] = facts.Health
						objs[i]["drain"] = facts.Drain
					}
				}
				return printJSON(objs)
			}
			if len(hosts) == 0 {
				say(cmd, "No hosts yet. Add one with `%s host add <name>`.\n", brand.Name)
				return nil
			}
			for _, h := range hosts {
				extra := ""
				if client.IsSessionMachineV1(h) {
					extra = "  (machine)"
				}
				if h.SSHConfig != "" {
					extra = "  (+ " + h.SSHConfig + ")"
				}
				facts, available := health[h.Name]
				if text := hostHealthTextV1(facts, available); text != "" {
					extra += "  (" + text + ")"
				}
				extra += hostLabelsSuffix(h)
				fmt.Printf("%-16s %-10s %s%s\n", h.Name, tiers.Of(h.Name), h.Target(), extra)
			}
			return nil
		},
	}
	addJSON(cmd, &asJSON)
	return cmd
}
func hostRekeyCmd() *cobra.Command {
	var hostKey, confirm string
	cmd := &cobra.Command{
		Use:               "rekey <host>",
		ValidArgsFunction: completeHostArg,
		Short:             "Replace a host's pinned host key, after you confirm",
		Long: "Read the host's key again, offering no key and no password, show the pinned key and the new one,\n" +
			"and pin the new one after you type the host's name. Run it only when you know why the key changed,\n" +
			"such as after you reinstalled the host. Otherwise, do not connect.",
		Example: "  " + brand.Name + " host rekey my-vps\n" +
			"  " + brand.Name + " host rekey my-vps --host-key SHA256:… --confirm my-vps",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return rekeyHost(cmd.Context(), prompts(cmd), args[0], hostKey, confirm)
		},
	}
	cmd.Flags().StringVar(&hostKey, "host-key", "", "the SHA256 `fingerprint` that the new key must have, as ssh-keygen -lf prints it on the host")
	addConfirm(cmd, &confirm, "the name of the `host`, typed here instead of at the question")
	return cmd
}

// rekeyKey replaces a host's pinned key. Tests replace it.
var rekeyKey = client.Rekey

// rekeyHost replaces the pinned key of the host called name (isolation
// spec 14.7.1): it shows the old key and the new one, and asks for the
// host's typed name, which --confirm gives instead. With input off and
// without --confirm, it fails with exit code 4 before it connects.
func rekeyHost(ctx context.Context, p *prompter, name, want, confirm string) error {
	h, err := client.FindHost(name)
	if err != nil {
		return err
	}
	if confirm != "" && confirm != name {
		return &client.UserError{Msg: fmt.Sprintf("--confirm names %s, and the host is %s", client.ShellWord(confirm), name),
			Hint: brand.Name + " host rekey " + name + " --confirm " + name, Code: "bad_value", Kind: client.KindUsage}
	}
	if confirm == "" && !p.on {
		return p.needs(qRekey, "the host's typed name to replace the pinned host key of "+name, "--confirm", name)
	}
	fmt.Fprintf(p.out, "Reading the host key of %s (%s)…\n", name, h.Target())
	oldKey, newKey, err := rekeyKey(ctx, h, want, func(oldKey, newKey string) error {
		if oldKey == "" {
			oldKey = "none"
		}
		w := max(len("Pinned now:"), len(name+" shows:"))
		fmt.Fprintf(p.out, "  %-*s %s\n  %-*s %s\n", w, "Pinned now:", oldKey, w, name+" shows:", newKey)
		if confirm != "" {
			return nil
		}
		ok, err := p.typedName(fmt.Sprintf("Type %s to pin the new key, or press Enter to keep the old one:", name), name)
		switch {
		case err != nil:
			return err
		case !ok:
			return p.cancel()
		case stopped(ctx):
			return errInterrupted // a signal that came with the name: change nothing
		}
		return nil
	})
	if err != nil {
		if isCancel(err) || ctx.Err() != nil {
			return p.cancelOf(err)
		}
		return err
	}
	if oldKey == newKey {
		fmt.Fprintf(p.out, "%s shows its pinned host key, %s, so nothing changed.\n", name, newKey)
		return nil
	}
	fmt.Fprintf(p.out, "  ✓ host key %s pinned for %s. The old key no longer works. Its tier is now shared.\n", newKey, name)
	return nil
}
