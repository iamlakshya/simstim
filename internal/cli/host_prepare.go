package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

type hostPrepareFlowOpts struct {
	explicitRoot     client.RootStepMode
	imagesMode       string
	delegateIO       bool
	nitroSlots       int
	separateAccounts bool
	yes              bool
	wake             bool
}

type prepareOutcomeView struct {
	tested         bool
	noRunnableWall bool
	levelAfterRoot string
	rootPrinted    bool
	result         api.SandboxPrepareResultV1
}

func viewPrepareOutcome(o *client.SandboxPrepareOutcomeV1) prepareOutcomeView {
	if o == nil {
		return prepareOutcomeView{}
	}
	return prepareOutcomeView{
		tested:         o.Tested(),
		noRunnableWall: o.NoRunnableWall(),
		levelAfterRoot: o.LevelAfterRoot(),
		rootPrinted:    o.RootPrinted(),
		result:         o.Result(),
	}
}

type hostPrepareSummary struct {
	root    client.HostPrepareRootOutcomeV1
	outcome prepareOutcomeView
	saved   client.Host
}

func (s hostPrepareSummary) tested() bool {
	return s.outcome.tested
}

func hostPrepareCmd() *cobra.Command {
	var rawMode, imagesMode string
	var nitroSlots int
	var delegateIO, separateAccounts, yes bool
	cmd := &cobra.Command{
		Use:               "prepare <host>",
		Short:             "Probe a host, prepare its runtime and test its sandbox wall",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeHostArg,
		Long: "Probe the host, install missing runtime files pinned by this client, handle\n" +
			"optional root steps, and run a short sandbox test boot before saving the\n" +
			"host's wall floor. A shared host asks before a runtime upgrade or root run.\n" +
			"No system package is installed. Running sessions continue.",
		Example: "  " + brand.Name + " host prepare my-vps\n" +
			"  " + brand.Name + " host prepare my-vps --root-step print\n" +
			"  " + brand.Name + " host prepare my-vps --root-step run --delegate-io\n" +
			"  " + brand.Name + " host prepare my-vps --images laptop",
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, err := parseHostPrepareFlags(cmd, rawMode, imagesMode, delegateIO, separateAccounts, yes, false)
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("nitro-slots") && (nitroSlots <= 0 || nitroSlots > 1<<20) {
				return &client.UserError{Msg: "the Nitro slot target must be a positive bounded number", Hint: "Use --nitro-slots with the required positive slot count.", Code: "nitro_slots", Kind: client.KindUsage}
			}
			opts.nitroSlots = nitroSlots
			p := prompts(cmd)
			baseCtx := cmd.Context()
			target, err := openHostPrepareTarget(baseCtx, args[0])
			if err != nil {
				return err
			}
			if _, err := runHostPrepareTargetV1(cmd, p, baseCtx, target, opts); err != nil {
				if isCancel(err) || baseCtx.Err() != nil {
					return p.cancelOf(err)
				}
				return err
			}
			return nil
		},
	}
	addRootStep(cmd, &rawMode, "handle optional root changes: run, print or skip (default depends on tier and sandbox history)")
	addImages(cmd, &imagesMode, "get runtime and image bytes from auto, host or laptop (default: [images] fetch)")
	cmd.Flags().BoolVar(&delegateIO, "delegate-io", false, "offer disk I/O controller delegation for this user")
	cmd.Flags().BoolVar(&separateAccounts, "separate-accounts", false, "offer the signed launcher and separate sandbox accounts")
	cmd.Flags().IntVar(&nitroSlots, "nitro-slots", 0, "offer a larger Nitro PCIe slot budget; donated memory stays until reboot")
	addYes(cmd, &yes, "print root steps without asking, unless --root-step explicitly selects a mode")
	return cmd
}

func parseHostPrepareFlags(cmd *cobra.Command, rawMode, imagesMode string, delegateIO, separateAccounts, yes, wake bool) (hostPrepareFlowOpts, error) {
	var mode client.RootStepMode
	if cmd.Flags().Changed("root-step") {
		var err error
		mode, err = client.ParseRootStepMode(rawMode)
		if err != nil {
			return hostPrepareFlowOpts{}, err
		}
	}
	if cmd.Flags().Changed("images") {
		if imagesMode == "" {
			return hostPrepareFlowOpts{}, &client.UserError{
				Msg:  `"" is not an images fetch mode`,
				Hint: "Use auto, host or laptop.",
				Code: "bad_images_fetch",
				Kind: client.KindUsage,
			}
		}
		var err error
		imagesMode, err = client.RuntimeFetchMode(imagesMode)
		if err != nil {
			return hostPrepareFlowOpts{}, err
		}
	}
	return hostPrepareFlowOpts{
		explicitRoot:     mode,
		imagesMode:       imagesMode,
		delegateIO:       delegateIO,
		separateAccounts: separateAccounts,
		yes:              yes,
		wake:             wake,
	}, nil
}

// Host preparation flow entry points, wired to the canonical H5/client APIs.
var (
	openHostPrepareTarget = client.OpenHostPrepareTargetV1
	probeHostPrepare      = client.ProbeHostPrepareV1
	runHostPrepareRoot    = client.RunHostPrepareRootPhaseV1
	bootHostPrepare       = func(ctx context.Context, target *client.HostPrepareTargetV1, explicitImages string, opts client.HostPrepareOptionsV1, wake, interactive bool, askGrant client.PrepareGrantFuncV1) (prepareOutcomeView, client.Host, error) {
		outcome, saved, err := client.BootAndRecordHostPrepareV1(ctx, target, explicitImages, opts, wake, interactive, askGrant)
		return viewPrepareOutcome(outcome), saved, err
	}
)

func runHostPrepareTargetV1(cmd *cobra.Command, p *prompter, baseCtx context.Context, target *client.HostPrepareTargetV1, opts hostPrepareFlowOpts) (hostPrepareSummary, error) {
	var summary hostPrepareSummary
	askGrant := func(gctx context.Context, run func(context.Context) error) (context.Context, error) {
		return grantQuestionsOn(cmd, p, gctx, run)
	}

	// Phase 1: capability probe on its own connection.
	probe, _, err := probeHostPrepare(baseCtx, target, opts.imagesMode, opts.wake, !opts.yes && p.on)
	if err != nil {
		return summary, err
	}
	printRootPrepareProbe(p.out, target.Name(), probe)

	// Phase 2: pinned runtime delivery under its own closed GrantRequest and connection.
	if _, err := prepareHostRuntimeTargetV1(cmd, p, baseCtx, target, opts.imagesMode, opts.wake); err != nil {
		return summary, err
	}

	// Phase 3: optional root step under its own closed GrantRequest and connection.
	rootOut, err := runHostPrepareRoot(baseCtx, target, client.HostPrepareRootOptionsV1{
		ExplicitMode:     opts.explicitRoot,
		DelegateIO:       opts.delegateIO,
		NitroSlots:       opts.nitroSlots,
		SeparateAccounts: opts.separateAccounts,
		Yes:              opts.yes,
		Wake:             opts.wake,
		Interactive:      p.on,
		In:               os.Stdin,
		Out:              p.out,
		ShowScript:       func(script client.RootScript) { showRootScript(p.out, target.Name(), script) },
		AskGrant:         askGrant,
		AuthoriseRun: func(h client.Host, tier client.Tier, rprobe api.CapProbe, explicit, yes bool) error {
			return authoriseRootRun(p, h, tier, rprobe, explicit, yes)
		},
	})
	if err != nil {
		return summary, err
	}
	summary.root = rootOut
	printHostPrepareRootResult(p, target.Name(), rootOut)

	// Phase 4: fixed sandbox test boot + floor/cache CAS only on verified success.
	outcome, saved, err := bootHostPrepare(baseCtx, target, opts.imagesMode, client.HostPrepareOptionsV1{
		Progress:    p.out,
		RootPrinted: rootOut.Printed,
	}, opts.wake, !opts.yes && p.on, askGrant)
	if err != nil {
		return summary, err
	}
	summary.outcome = outcome
	summary.saved = saved
	printHostPrepareOutcome(p, target.Name(), rootOut, outcome, saved)
	return summary, nil
}

func printHostPrepareRootResult(p *prompter, hostName string, rootOut client.HostPrepareRootOutcomeV1) {
	switch {
	case !rootOut.Needed:
		fmt.Fprintln(p.out, "No supported root step is needed.")
	case rootOut.Ran:
		p.changes().Add("ran the root steps on %s", hostName)
		fmt.Fprintln(p.out, "The fresh sandbox probe confirmed every root step.")
	case rootOut.Printed:
		p.changes().Add("wrote the root script %s", rootOut.ScriptFile)
		fmt.Fprintf(p.out, "Wrote %s (0600).\nSHA-256 %s\nGive the script to %s's owner to review and run with sudo.\n",
			rootOut.ScriptFile, rootOut.SHA256, client.CleanLine(hostName))
		fmt.Fprintf(p.out, "Then run %s host prepare %s --root-step skip to test and record the wall.\n",
			brand.Name, shellWord(hostName))
	case rootOut.Skipped:
		fmt.Fprintln(p.out, "Root steps skipped. Testing the host's current sandbox wall.")
	}
}

func printHostPrepareOutcome(p *prompter, hostName string, rootOut client.HostPrepareRootOutcomeV1, outcome prepareOutcomeView, saved client.Host) {
	cleanHost := client.CleanLine(hostName)
	if outcome.tested {
		res := outcome.result
		level, driver := client.CleanLine(res.Level), client.CleanLine(res.Driver)
		floor := client.CleanLine(saved.WallFloor)
		p.changes().Add("prepared the %s sandbox wall on %s", level, hostName)
		if floor != "" && floor != level {
			fmt.Fprintf(p.out, "Sandbox wall %s (%s) tested in %d ms on %s. Existing stronger floor %s kept.\n",
				level, driver, res.BootMS, cleanHost, floor)
		} else {
			fmt.Fprintf(p.out, "Sandbox wall %s (%s) tested in %d ms on %s. Wall floor: %s.\n",
				level, driver, res.BootMS, cleanHost, floor)
		}
		if after := client.CleanLine(outcome.levelAfterRoot); after != "" && after != level && !rootOut.Ran {
			fmt.Fprintf(p.out, "Optional root steps can raise %s to %s.\n", cleanHost, after)
		}
		return
	}
	fmt.Fprintf(p.out, "No sandbox wall can run on %s yet. Its wall floor was not changed.\n", cleanHost)
	after := client.CleanLine(outcome.levelAfterRoot)
	if after == "" {
		return
	}
	if outcome.rootPrinted {
		fmt.Fprintf(p.out, "Run the printed root script on %s to enable %s, then run %s host prepare %s --root-step skip.\n",
			cleanHost, after, brand.Name, shellWord(hostName))
		return
	}
	fmt.Fprintf(p.out, "Run %s host prepare %s to inspect the root steps for %s.\n",
		brand.Name, shellWord(hostName), after)
}

func printRootPrepareProbe(w io.Writer, host string, probe api.CapProbe) {
	clean := client.CleanCapProbe(probe)
	fmt.Fprintf(w, "%s: %s/%s, account %s (uid %d), %d recorded sandbox starts\n",
		client.CleanLine(host), clean.OS, clean.Arch, clean.User, clean.UID, clean.Started)
	for _, line := range client.FormatSandboxProbeLinesV1(clean) {
		fmt.Fprintf(w, "  %s\n", line)
	}
	for _, note := range client.RootPackageNotes(probe) {
		fmt.Fprintf(w, "%s: %s\n", note.Label, note.Value)
	}
}

func showRootScript(w io.Writer, host string, script client.RootScript) {
	fmt.Fprintf(w, "\nRoot steps on %s\n", client.CleanLine(host))
	for index, step := range script.Steps {
		wide := ""
		if step.MachineWide {
			wide = " (machine-wide)"
		}
		fmt.Fprintf(w, "  %d %s%s\n    %s\n    %s\n    Undo: %s\n", index+1, step.ID, wide, step.Line, step.Reason, step.Undo)
	}
	fmt.Fprintln(w, "\nExact script:")
	fmt.Fprint(w, script.Text)
	fmt.Fprintf(w, "\nSHA-256 %s\n", script.SHA256)
}

func authoriseRootRun(p *prompter, h client.Host, tier client.Tier, probe api.CapProbe, explicit, yes bool) error {
	printHint := brand.Name + " host prepare " + shellWord(h.Name) + " --root-step print"
	if !probe.Sudo {
		return &client.UserError{Msg: "this account has no sudo for its root step", Hint: printHint + " (give the script to the machine's owner)", Code: "root_step_sudo", Kind: client.KindFailed}
	}
	if yes && !probe.SudoPasswordless {
		return &client.UserError{Msg: "sudo needs a password, and --yes means that nobody types it", Hint: printHint, Code: "root_step_password", Kind: client.KindFailed}
	}
	if !p.on && !yes {
		return &client.UserError{Msg: "the root step needs a visible terminal for sudo", Hint: printHint, Code: "root_step_terminal", Kind: client.KindNeedsAnswer}
	}
	// Shared-host name consent has already passed the scoped client gate.
	if (tier == client.TierOwn || tier == client.TierThrowaway) && !explicit && !yes {
		ok, err := p.confirm(qRootStep, "Run these root steps with sudo on "+client.CleanLine(h.Name)+"?", "a yes to run the root step")
		if err != nil {
			return err
		}
		if !ok {
			return p.cancel()
		}
	}
	if stopped(p.ctx) {
		return errInterrupted
	}
	return nil
}
