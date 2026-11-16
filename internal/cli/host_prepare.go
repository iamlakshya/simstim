package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func hostPrepareCmd() *cobra.Command {
	var rawMode string
	var delegateIO, yes bool
	cmd := &cobra.Command{
		Use: "prepare <host>", Short: "Probe a host and show its optional root steps", Args: cobra.ExactArgs(1),
		ValidArgsFunction: completeHostArg,
		Long: "Show each optional root change with its reason and undo. Run the fixed script in your terminal,\n" +
			"print it for the machine's owner, or skip it. No package is installed. Running sessions continue.",
		Example: "  " + brand.Name + " host prepare my-vps --root-step print\n  " + brand.Name + " host prepare my-vps --root-step run --delegate-io",
		RunE: func(cmd *cobra.Command, args []string) (returned error) {
			var mode client.RootStepMode
			if cmd.Flags().Changed("root-step") {
				var err error
				mode, err = client.ParseRootStepMode(rawMode)
				if err != nil {
					return err
				}
			}
			// This immutable purpose exists before any context or question.
			request := client.NewRootStepGrantRequest()
			original := cmd.Context()
			cmd.SetContext(client.WithGrantRequest(original, request))
			defer func() {
				returned = errors.Join(returned, request.Close())
				cmd.SetContext(original)
			}()
			return prepareHost(cmd, args[0], mode, delegateIO, yes)
		},
	}
	cmd.Flags().StringVar(&rawMode, "root-step", "", "handle optional root changes: run, print or skip (default depends on tier and sandbox history)")
	cmd.Flags().BoolVar(&delegateIO, "delegate-io", false, "offer disk I/O controller delegation for this user")
	addYes(cmd, &yes, "print root steps without asking, unless --root-step explicitly selects a mode")
	return cmd
}

func prepareHost(cmd *cobra.Command, name string, explicit client.RootStepMode, delegateIO, originalYes bool) error {
	ctx := cmd.Context()
	h, err := client.FindHost(name)
	if err != nil {
		return err
	}
	if _, err := client.ResolveHostTier(ctx, h); err != nil {
		return err
	}
	// Dial and every terminal question run without a host publication lock.
	// The actual client producer retains this accepted connection's Source.
	conn, err := client.Dial(client.WithoutWake(ctx), h, !originalYes && inputOn(cmd))
	if err != nil {
		return err
	}
	defer conn.Close()
	probe, err := conn.CapProbe(ctx, "")
	if err != nil {
		return err
	}
	printRootPrepareProbe(prompts(cmd).out, h.Name, probe)
	tier, hostID, err := client.HostGrantTier(ctx, h)
	if err != nil {
		return err
	}
	if !client.ValidHostID(hostID) || hostID != conn.HostID() {
		return &client.UserError{Msg: "the host identity changed before its root step", Hint: brand.Name + " host test " + shellWord(name), Code: "root_step_identity", Kind: client.KindRefused}
	}
	// No runtime is fetched or presumed verified by this optional command.
	// RootStepsFor also requires the probe's actual driver facts for KVM.
	ids := client.RootStepsFor(probe, api.RuntimeStatus{}, delegateIO, 0)
	if len(ids) == 0 {
		fmt.Fprintln(prompts(cmd).out, "No supported root step is needed.")
		return nil
	}
	script, err := client.BuildRootScript(probe.User, probe.UID, ids, 0)
	if err != nil {
		return err
	}
	started, err := client.RootHistoryStarted(ctx, h, hostID)
	if err != nil {
		return err
	}
	mode := explicit
	if mode == "" {
		mode = client.DefaultRootStepMode(tier, started, probe, originalYes)
	}
	showRootScript(prompts(cmd).out, h.Name, script)
	// The same script/mode/yes values survive every terminal answer. The
	// client seals them before asking and rechecks fresh facts at native Start.
	if err := grantQuestions(cmd, func(ctx context.Context) error {
		return conn.CheckRootStepGrant(ctx, script, mode, originalYes)
	}); err != nil {
		return err
	}
	if mode == client.RootRun {
		if err := authoriseRootRun(prompts(cmd), h, tier, probe, explicit != "", originalYes); err != nil {
			return err
		}
		if err := conn.RunRootStep(cmd.Context(), script, mode, client.RootRunOptions{Passwordless: originalYes, In: os.Stdin, Out: prompts(cmd).out}); err != nil {
			return err
		}
		fmt.Fprintln(prompts(cmd).out, "The fresh sandbox probe confirmed every root step.")
		return nil
	}
	status := "skipped"
	file := ""
	if mode == client.RootPrint {
		file, err = conn.WriteRootScript(cmd.Context(), script)
		if err != nil {
			return err
		}
		status = "printed"
	}
	if err := client.RecordRootStep(cmd.Context(), h, client.RootStepRecord{HostID: hostID, Steps: ids, SHA256: script.SHA256,
		Mode: mode, At: time.Now().UTC(), Status: status, StartedSeen: started || probe.Started != 0}); err != nil {
		return err
	}
	if mode == client.RootPrint {
		fmt.Fprintf(prompts(cmd).out, "Wrote %s (0600).\nSHA-256 %s\nRun on %s: sudo sh %s\n", file, script.SHA256, client.CleanLine(h.Name), client.ShellQuote(file))
		fmt.Fprintf(prompts(cmd).out, "Then run %s host prepare %s --root-step skip to probe again.\n", brand.Name, shellWord(h.Name))
	} else {
		fmt.Fprintln(prompts(cmd).out, "Root steps skipped. The host keeps its current level.")
	}
	return nil
}

func printRootPrepareProbe(w io.Writer, host string, probe api.CapProbe) {
	clean := client.CleanCapProbe(probe)
	fmt.Fprintf(w, "%s: %s/%s, account %s (uid %d), %d recorded sandbox starts\n", client.CleanLine(host), clean.OS, clean.Arch, clean.User, clean.UID, clean.Started)
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
