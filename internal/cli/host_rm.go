package cli

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func hostRmCmd() *cobra.Command {
	var opts hostRmOptions
	cmd := &cobra.Command{
		Use:   "rm <host>",
		Short: "Forget a host, and optionally delete its created machine",
		Long: "Removes a host from hosts.toml and cleans up its generated SSH config and pinned\n" +
			"host key. On a host made by `host create`, at a terminal it asks whether to delete\n" +
			"the machine too. Pass --destroy to delete the machine (and --confirm <host> when\n" +
			"input is off). Refuses to delete a machine while a session is working unless --force.",
		Example: "  " + brand.Name + " host rm my-vps\n" +
			"  " + brand.Name + " host rm my-vps --destroy --confirm my-vps",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return rmHost(cmd.Context(), prompts(cmd), args[0], opts)
		},
	}
	cmd.Flags().BoolVar(&opts.destroy, "destroy", false, "delete the machine that `host create` made, then forget the host")
	addForce(cmd, &opts.force, "delete the machine even while a session is working (with --destroy)")
	addConfirm(cmd, &opts.confirm, "type the host's name here instead of at the prompt (with --destroy)")
	return cmd
}

func hostDestroyCmd() *cobra.Command {
	var force, yes bool
	cmd := &cobra.Command{
		Use:    "destroy <host>",
		Short:  "Delete a created host: its machine and every session on it",
		Hidden: true,
		Long: "Delete the machine behind a host, then forget the host. It asks first, and refuses while a\n" +
			"session is working unless you pass --force.",
		Example: "  " + brand.Name + " host destroy lima",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			warnOld(cmd, oldHostDestroy)
			return destroyHost(cmd.Context(), prompts(cmd), args[0], force, yes)
		},
	}
	addForce(cmd, &force, "destroy it even while a session is working")
	addYes(cmd, &yes, "do not ask first")
	return cmd
}

type hostRmOptions struct {
	destroy    bool
	force      bool
	confirm    string
	viaDestroy bool
}

// destroyHost deletes the machine behind the host name and forgets the host.
// It implements the `host destroy` warned alias of `host rm --destroy`.
func destroyHost(ctx context.Context, p *prompter, name string, force, yes bool) error {
	opts := hostRmOptions{destroy: true, force: force, viaDestroy: true}
	if yes {
		opts.confirm = name
	}
	return rmHost(ctx, p, name, opts)
}

func rmHost(ctx context.Context, p *prompter, name string, opts hostRmOptions) error {
	if err := checkRmFlags(name, opts); err != nil {
		return err
	}
	if _, err := findHostForRm(name); err != nil {
		return err
	}
	unlock, err := client.LockHost(ctx, name, "host rm")
	if err != nil {
		return err
	}
	defer unlock()

	h, err := findHostForRm(name)
	if err != nil {
		return err
	}
	prev := cloneHost(h)

	if opts.confirm != "" && opts.confirm != h.Name {
		return &client.UserError{
			Msg:  fmt.Sprintf("--confirm %q does not match the host name %q", opts.confirm, h.Name),
			Hint: fmt.Sprintf("%s host rm %s --destroy --confirm %s", brand.Name, client.ShellQuote(h.Name), client.ShellQuote(h.Name)),
			Code: "bad_value",
			Kind: client.KindUsage,
		}
	}
	if opts.destroy && h.Machine == nil {
		return &client.UserError{
			Msg:  h.Name + " was not made by host create, so there is no machine to delete",
			Hint: brand.Name + " host rm " + client.ShellQuote(h.Name),
			Code: "not_created",
			Kind: client.KindUsage,
		}
	}

	// Plain added host, or non-interactive forget of a created host without --destroy.
	if !opts.destroy && (h.Machine == nil || !p.on) {
		return forgetOnly(ctx, p, prev)
	}

	lc, err := client.OpenMachine(h)
	if err != nil {
		return err
	}

	if !opts.destroy {
		return rmInteractiveCreated(ctx, p, prev, lc)
	}
	return rmDestroyCreated(ctx, p, prev, lc, opts)
}

func checkRmFlags(name string, opts hostRmOptions) error {
	q := client.ShellQuote(name)
	if opts.confirm != "" && !opts.destroy {
		return &client.UserError{
			Msg:  "--confirm needs --destroy",
			Hint: fmt.Sprintf("%s host rm %s --destroy --confirm %s", brand.Name, q, q),
			Code: "bad_value",
			Kind: client.KindUsage,
		}
	}
	if opts.force && !opts.destroy {
		return &client.UserError{
			Msg:  "--force needs --destroy",
			Hint: fmt.Sprintf("%s host rm %s --destroy --force", brand.Name, q),
			Code: "bad_value",
			Kind: client.KindUsage,
		}
	}
	return nil
}

func findHostForRm(name string) (client.Host, error) {
	h, err := client.FindHost(name)
	if err != nil {
		var un *client.UnknownNameError
		if errors.As(err, &un) {
			un.Hint = brand.Name + " host ls"
		}
		return client.Host{}, err
	}
	return h, nil
}

func forgetOnly(ctx context.Context, p *prompter, h client.Host) error {
	if err := client.ForgetHostIfUnchanged(ctx, h); err != nil {
		return err
	}
	fmt.Fprintf(p.out, "Forgot %s. Its sessions and the helper stay on the host.\n", h.Name)
	if m := h.Machine; m != nil {
		q := client.ShellQuote(h.Name)
		fmt.Fprintf(p.out, "Its %s machine %s is kept. Pass `--destroy --confirm %s` to delete it next time; now delete it with %s's own tools.\n", m.Provider, m.ID, q, m.Provider)
	}
	return nil
}

func rmInteractiveCreated(ctx context.Context, p *prompter, prev client.Host, lc client.Lifecycle) error {
	m := prev.Machine
	insp := inspectMachine(ctx, prev, lc)
	desc := fmt.Sprintf("%s was made by `host create`: the %s machine %s.", prev.Name, m.Provider, m.ID)
	switch {
	case insp.statusErr != nil:
		desc += " Could not check its state."
	case insp.status == client.MachineMissing:
		desc += " The machine is already gone."
	case insp.status != client.MachineRunning:
		desc += fmt.Sprintf(" It is %s.", insp.status)
	case insp.sessionsErr != nil:
		desc += " Could not check its sessions."
	case insp.total == 1:
		desc += " It holds 1 session."
	default:
		desc += fmt.Sprintf(" It holds %d sessions.", insp.total)
	}
	fmt.Fprintln(p.out, desc)
	ok, err := p.typedName(fmt.Sprintf("Type %s to delete the machine too, or press Enter to keep it:", prev.Name), prev.Name)
	if err != nil {
		return err
	}
	if !ok {
		return forgetOnly(ctx, p, prev)
	}
	if stopped(ctx) {
		return errInterrupted
	}
	if err := recheckBeforeDestroy(ctx, prev, lc, false, false); err != nil {
		return err
	}
	return destroyAndForget(ctx, p, prev, lc)
}

func rmDestroyCreated(ctx context.Context, p *prompter, prev client.Host, lc client.Lifecycle, opts hostRmOptions) error {
	if !opts.force {
		if err := refuseIfWorking(ctx, prev, lc, opts.viaDestroy); err != nil {
			return err
		}
	}
	if opts.confirm == "" {
		what := fmt.Sprintf("the %s machine %s", prev.Machine.Provider, prev.Machine.ID)
		if !p.on {
			if opts.viaDestroy {
				return p.needs(qDestroy, "a yes to delete "+what)
			}
			return p.needs(qHostRmDestroy, "the typed host name to delete "+what, "--confirm", prev.Name)
		}
		ok, err := p.typedName(fmt.Sprintf("Type %s to delete the machine, or press Enter to keep it:", prev.Name), prev.Name)
		if err != nil {
			return err
		}
		if !ok {
			return p.cancel()
		}
		if stopped(ctx) {
			return errInterrupted
		}
		if err := recheckBeforeDestroy(ctx, prev, lc, opts.force, opts.viaDestroy); err != nil {
			return err
		}
	}
	return destroyAndForget(ctx, p, prev, lc)
}

func recheckBeforeDestroy(ctx context.Context, prev client.Host, lc client.Lifecycle, force, viaDestroy bool) error {
	cur, err := client.FindHost(prev.Name)
	if err != nil || !sameMachine(prev, cur) {
		return &client.UserError{
			Msg:  prev.Name + " changed while waiting for confirmation, so nothing was deleted",
			Hint: brand.Name + " host show " + client.ShellQuote(prev.Name),
			Code: "host_changed",
		}
	}
	if !force {
		return refuseIfWorking(ctx, prev, lc, viaDestroy)
	}
	return nil
}

func sameMachine(a, b client.Host) bool {
	if a.Machine == nil || b.Machine == nil {
		return false
	}
	return a.Machine.Provider == b.Machine.Provider &&
		a.Machine.ID == b.Machine.ID &&
		a.Machine.Created == b.Machine.Created
}

func destroyAndForget(ctx context.Context, p *prompter, h client.Host, lc client.Lifecycle) error {
	m := h.Machine
	what := fmt.Sprintf("the %s machine %s", m.Provider, m.ID)

	rec, hasRec, _ := client.LoadRemovalRecord(h.Name)
	sctx, scancel := context.WithTimeout(ctx, 30*time.Second)
	st, stErr := lc.Status(sctx)
	scancel()

	alreadyGone := stErr == nil && st == client.MachineMissing
	if !alreadyGone {
		dctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		err := lc.Destroy(dctx)
		cancel()
		if err != nil {
			return err
		}
		_ = client.SaveRemovalRecord(client.RemovalRecord{
			Host:           h.Name,
			Provider:       m.Provider,
			MachineID:      m.ID,
			MachineCreated: m.Created,
			Destroyed:      true,
			UpdatedAt:      time.Now().UTC(),
		})
	} else if hasRec && rec.Destroyed && rec.Provider == m.Provider && rec.MachineID == m.ID && rec.MachineCreated == m.Created {
		fmt.Fprintf(p.out, "An earlier run already deleted %s.\n", what)
	}

	(&client.SSH{Host: h}).ExitMaster(context.WithoutCancel(ctx))
	if err := client.ForgetHost(ctx, h.Name); err != nil {
		return err
	}
	fmt.Fprintf(p.out, "Deleted %s and forgot %s.\n", what, h.Name)
	return nil
}

type machineInspection struct {
	status      string
	statusErr   error
	total       int
	working     []string
	sessionsErr error
}

// inspectMachine asks the provider for h's status and, when running, lists its
// sessions without waking, installing or pruning a helper (cli-spec 11.3, rule 9).
func inspectMachine(ctx context.Context, h client.Host, lc client.Lifecycle) machineInspection {
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	st, err := lc.Status(sctx)
	if err != nil {
		return machineInspection{statusErr: err}
	}
	if st != client.MachineRunning {
		return machineInspection{status: st}
	}
	conn, err := client.Dial(client.WithoutInstall(client.WithoutWake(sctx)), h, false)
	if err != nil {
		return machineInspection{status: st, sessionsErr: err}
	}
	defer conn.Close()
	sessions, err := conn.Sessions(sctx)
	if err != nil {
		return machineInspection{status: st, sessionsErr: err}
	}
	var working []string
	for _, s := range sessions {
		if s.Alive && (s.State == api.StateWorking || s.State == api.StateStarting) {
			working = append(working, api.CleanText(s.Name, 200))
		}
	}
	sort.Strings(working)
	return machineInspection{status: st, total: len(sessions), working: working}
}

func forceHint(name string, viaDestroy bool) string {
	q := client.ShellQuote(name)
	if viaDestroy {
		return brand.Name + " host destroy " + q + " --force (stops them too)"
	}
	return brand.Name + " host rm " + q + " --destroy --force (stops them too)"
}

func refuseIfWorking(ctx context.Context, h client.Host, lc client.Lifecycle, viaDestroy bool) error {
	insp := inspectMachine(ctx, h, lc)
	hint := forceHint(h.Name, viaDestroy)
	if insp.statusErr != nil {
		return &client.UserError{
			Msg:  fmt.Sprintf("cannot check whether %s is running (%v), so a session may be working", h.Name, insp.statusErr),
			Hint: hint,
			Code: "sessions_working",
		}
	}
	if insp.sessionsErr != nil {
		return &client.UserError{
			Msg:  fmt.Sprintf("cannot list sessions on running host %s (%v), so a session may be working", h.Name, insp.sessionsErr),
			Hint: hint,
			Code: "sessions_working",
		}
	}
	if len(insp.working) > 0 {
		return &client.UserError{
			Msg:  fmt.Sprintf("%d session(s) on %s are working: %s", len(insp.working), h.Name, strings.Join(insp.working, ", ")),
			Hint: hint,
			Code: "sessions_working",
		}
	}
	return nil
}
