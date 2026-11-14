package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// The removal of a host (docs/contributing/cli-spec.md 11.3). host rm
// forgets a host, and with --destroy, or the typed name at a terminal, it
// also deletes the machine that `host create` made. host destroy is its
// old name. What host rm sees on the machine is in host_rm_look.go.

// rmOpts are the words and flags of host rm.
type rmOpts struct {
	name    string
	destroy bool
	force   bool
	// confirm is the name that --confirm gave, and confirmed is true when
	// the flag was given.
	confirm   string
	confirmed bool
}

func hostRmCmd() *cobra.Command {
	var o rmOpts
	cmd := &cobra.Command{
		Use:   "rm <host>",
		Short: "Forget a host, or delete a created machine",
		Long: "Forget a host: it leaves hosts.toml. Its sessions and the helper stay on the host.\n" +
			"A host made by `host create` also has a machine. At a terminal, rm says what the machine\n" +
			"holds, and asks you to type the host's name to delete it too. --destroy deletes it: it\n" +
			"needs the typed name, or --confirm with the host's name. It refuses while a session is\n" +
			"working, unless you pass --force.",
		Example: "  " + brand.Name + " host rm my-vps\n  " + brand.Name + " host rm lima --destroy --confirm lima",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o.name, o.confirmed = args[0], cmd.Flags().Changed("confirm")
			return removeHost(cmd.Context(), prompts(cmd), o)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&o.destroy, "destroy", false, "also delete the machine that host create made: its VM, container or sandbox, with its disk and every session on it")
	addForce(cmd, &o.force, "with --destroy, delete the machine although a session is working")
	addConfirm(cmd, &o.confirm, "with --destroy, the host's `name`, typed in place of the question")
	return cmd
}

// hostDestroyCmd is the old name of host rm --destroy. Its -y is now
// --confirm <host>, and its --force stays.
func hostDestroyCmd() *cobra.Command {
	var o rmOpts
	var yes bool
	cmd := &cobra.Command{
		Use:     "destroy <host>",
		Short:   "Delete a host made by `host create` (old name of host rm --destroy)",
		Hidden:  true,
		Example: "  " + brand.Name + " host rm lima --destroy",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			warnOld(cmd, oldHostDestroy)
			o.name, o.destroy = args[0], true
			o.confirm, o.confirmed = args[0], yes
			return removeHost(cmd.Context(), prompts(cmd), o)
		},
	}
	addForce(cmd, &o.force, "destroy it even while a session is working")
	addYes(cmd, &yes, "the same as --confirm with the host's name")
	return cmd
}

// line is o as a command line in its new form, with more flags after it:
// a Try: line that runs as written.
func (o rmOpts) line(more ...string) string {
	parts := []string{brand.Name, "host", "rm", shellWord(o.name)}
	if o.destroy {
		parts = append(parts, "--destroy")
	}
	if o.force {
		parts = append(parts, "--force")
	}
	if o.confirmed {
		parts = append(parts, "--confirm", shellWord(o.name))
	}
	return strings.Join(append(parts, more...), " ")
}

// removeHost forgets the host that o names, and deletes its machine when
// o or the answer to p's question says so. The work goes in order: the
// machine, then the host. Each part that ends is recorded, so that a run
// after a failure knows what it finishes (cli-spec 11.3, rule 10).
func removeHost(ctx context.Context, p *prompter, o rmOpts) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !o.destroy && (o.confirmed || o.force) {
		return withoutDestroy(o)
	}
	h, err := client.FindHost(o.name)
	if err != nil {
		return unknownToRm(err, o)
	}
	if o.confirmed && o.confirm != h.Name {
		return &client.UserError{Msg: fmt.Sprintf("--confirm got %q, which is not the name of the host %s", o.confirm, h.Name),
			Hint: o.line(), Code: "confirm_mismatch", Kind: client.KindUsage}
	}
	if o.destroy && h.Machine == nil {
		return &client.UserError{Msg: h.Name + " was not made by host create, so there is no machine to delete",
			Hint: rmOpts{name: h.Name}.line(), Code: "not_created", Kind: client.KindUsage}
	}
	// The lock of the host, from the first look through the question, the
	// delete, the record and the forget: no other command changes the host
	// meanwhile, and every look happens under it.
	beforeHostLock(h.Name)
	ctx, unlock, err := client.LockHost(ctx, h.Name, "host rm")
	if err != nil {
		return err
	}
	defer unlock()
	if err := sameHost(h, o); err != nil {
		return err
	}
	rec := client.LoadRemoval(h)
	m, err := machineToRemove(ctx, p, o, h, &rec)
	if err != nil {
		return err
	}
	destroy, asked, err := m.decide(p, o)
	if err != nil {
		return err
	}
	// A cheap guard: under the lock, the host is still the one that host
	// rm looked at.
	if err := sameHost(h, o); err != nil {
		return err
	}
	if destroy && asked && !o.force {
		// The question may have waited long: look again, just before the
		// delete.
		if destroy, err = m.lookAgain(ctx, p, o, &rec); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if destroy {
		if err := m.destroy(ctx, p, &rec); err != nil {
			return err
		}
	}
	// The forget compares with the entry that this command read.
	if err := forgetRemovedHost(ctx, p, h, destroy || m.gone); err != nil {
		return err
	}

	switch {
	case h.Machine == nil:
		fmt.Fprintf(p.out, "Forgot %s. Its sessions and the helper stay on the host.\n", h.Name)
	case !m.gone && !destroy:
		fmt.Fprintf(p.out, "Forgot %s. %s stays, with its sessions and the helper: delete it with %s's own tools.\n",
			h.Name, sentenceCase(m.what()), h.Machine.Provider)
	default:
		fmt.Fprintf(p.out, "Forgot %s.\n", h.Name)
	}
	return nil
}

// beforeHostLock runs just before host rm or host set takes the lock of
// the host. Tests change the host there.
var beforeHostLock = func(string) {}

// withoutDestroy is the error of --confirm or --force without --destroy:
// both act only on the delete of the machine (cli-spec 11.3).
func withoutDestroy(o rmOpts) error {
	var flags []string
	if o.confirmed {
		flags = append(flags, "--confirm")
	}
	if o.force {
		flags = append(flags, "--force")
	}
	o.destroy = true
	return &client.UserError{Msg: strings.Join(flags, " and ") + " needs --destroy", Hint: o.line(), Code: "missing_flag", Kind: client.KindUsage}
}

// unknownToRm is the error of a host that host rm cannot find. Its Try:
// line is `host ls`, not `host add`. With --confirm, the line that names
// the nearest host names it in --confirm too, so that it runs as written.
func unknownToRm(err error, o rmOpts) error {
	var unknown *client.UnknownNameError
	if !errors.As(err, &unknown) {
		return err
	}
	unknown.Hint = brand.Name + " host ls"
	if !o.confirmed {
		return err
	}
	things := make([]named, len(unknown.Known))
	for i, k := range unknown.Known {
		things[i] = named{k, []string{k}}
	}
	cands := suggest(unknown.Name, things, false)
	ue := *unknown.UserError
	if len(cands) > 0 {
		o.name = cands[0]
		ue.Hint = o.line()
	}
	return &usageError{UserError: &ue, suggestions: cands}
}

// sameHost refuses when the saved entry of h is no longer h: another
// command removed the host, made it again, or changed it, while host rm
// looked or asked. A host made again by Lima or Docker has the same
// machine ID, so its provider, ID and created stamp all count. So does
// every other field of the entry, which host rm would forget.
func sameHost(h client.Host, o rmOpts) error {
	now, err := client.FindHost(h.Name)
	var unknown *client.UnknownNameError
	switch {
	case errors.As(err, &unknown):
		return &client.UserError{Msg: h.Name + " was removed while host rm looked at it", Hint: brand.Name + " host ls", Code: "host_changed"}
	case err != nil:
		// Such as a hosts.toml that does not decode: its own error says
		// where.
		return err
	}
	if client.SameEntry(now, h) {
		return nil
	}
	what := "changed"
	if !client.SameMachine(now.Machine, h.Machine) {
		what = "was made again"
	}
	o.name = h.Name
	return &client.UserError{Msg: fmt.Sprintf("%s %s while host rm looked at it, so it changed nothing", h.Name, what), Hint: o.line(), Code: "host_changed"}
}

// rmMachine is the machine of a host that host rm removes, and what host
// rm saw on it.
type rmMachine struct {
	h client.Host
	// lc is the machine's lifecycle, or nil when there is no machine that
	// host rm may delete.
	lc client.Lifecycle
	// gone is true when the provider says that the machine is gone.
	gone bool
	machineLook
}

// machineToRemove opens the machine of h, and looks at it, when host rm
// may delete it: with --destroy, or with input on, to ask. With input off
// and without --destroy, the machine stays, and it says so. A record of
// an earlier run never decides that the machine is gone: only the
// provider does. Without a machine to delete, lc is nil.
func machineToRemove(ctx context.Context, p *prompter, o rmOpts, h client.Host, rec *client.Removal) (rmMachine, error) {
	m := rmMachine{h: h}
	if h.Machine == nil {
		return m, nil
	}
	asks := p.asks(qRmMachine)
	if o.destroy && !o.confirmed && !asks {
		return m, &client.UserError{
			Msg:  fmt.Sprintf("host rm needs the typed name of %s to delete %s, and input is off", h.Name, m.what()),
			Hint: o.line("--confirm", shellWord(h.Name)),
			Code: "needs_answer", Kind: client.KindNeedsAnswer,
		}
	}
	keeps := !o.destroy && !asks
	if keeps && !rec.Has(client.RemovalDestroy) {
		m.keep(p)
		return m, nil
	}
	lc, err := client.OpenMachine(h)
	if err != nil {
		if o.destroy {
			return m, err
		}
		// This build cannot reach the machine: forget the host alone.
		fmt.Fprintf(p.out, "%s cannot reach the machine of %s: %s\n", brand.DisplayName, h.Name, shown(err.Error()))
		return m, nil
	}
	if keeps {
		// Only the record of an earlier run brings a script here: the
		// provider says whether that run deleted the machine.
		m.machineLook = lookAtState(ctx, lc)
	} else {
		m.machineLook = lookAtMachine(ctx, h, lc)
	}
	if err := ctx.Err(); err != nil {
		return m, err
	}
	if m.state == client.MachineMissing {
		return m, m.goneAlready(p, rec)
	}
	if keeps {
		m.keep(p)
		return m, nil
	}
	m.lc = lc
	return m, nil
}

// keep says that host rm keeps the machine, with input off and without
// --destroy, and which flags delete it too.
func (m *rmMachine) keep(p *prompter) {
	fmt.Fprintf(p.out, "Input is off, so host rm keeps %s. With --destroy --confirm %s, it deletes the machine too.\n", m.what(), shellWord(m.h.Name))
}

// goneAlready records a machine that its provider no longer has, which
// counts as deleted, and says so.
func (m *rmMachine) goneAlready(p *prompter, rec *client.Removal) error {
	m.gone, m.lc = true, nil
	why := "is gone already, so there is nothing to delete"
	if rec.Has(client.RemovalDestroy) {
		why = "was deleted by an earlier run"
	}
	if err := rec.Record(client.RemovalDestroy); err != nil {
		return err
	}
	fmt.Fprintf(p.out, "%s %s.\n", sentenceCase(m.what()), why)
	return nil
}

// what names the machine: "the lima machine box-1a2b".
func (m *rmMachine) what() string {
	return fmt.Sprintf("the %s machine %s", m.h.Machine.Provider, shown(m.h.Machine.ID))
}

// decide says whether host rm deletes the machine that it may delete
// (machineToRemove): with --destroy and --confirm, or when the user types
// the host's name at its question. asked is true when it asked. With
// --destroy, it refuses before the question while a session works, unless
// --force.
func (m *rmMachine) decide(p *prompter, o rmOpts) (destroy, asked bool, err error) {
	if m.lc == nil {
		return false, false, nil
	}
	if o.destroy && !o.force {
		// Refuse before the question, which could not change it.
		if err := m.refusal(o); err != nil {
			return false, false, err
		}
	}
	if o.destroy && o.confirmed {
		return true, false, nil
	}
	ok, err := m.ask(p, o)
	return ok, true, err
}

// lookAgain looks at the machine once more, after a question that may
// have waited long, just before the delete. It says whether the delete
// goes on: not when the machine is gone. It refuses while a session works,
// or when either check fails.
func (m *rmMachine) lookAgain(ctx context.Context, p *prompter, o rmOpts, rec *client.Removal) (bool, error) {
	m.machineLook = lookAtMachine(ctx, m.h, m.lc)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if m.state == client.MachineMissing {
		return false, m.goneAlready(p, rec)
	}
	if err := m.refusal(o); err != nil {
		return false, err
	}
	return true, nil
}

// ask says what the machine holds, and asks for the host's typed name
// (G9). Enter keeps the machine. With --destroy, which asked to delete it,
// Enter then cancels the command, and nothing changes.
func (m *rmMachine) ask(p *prompter, o rmOpts) (bool, error) {
	fmt.Fprintf(p.out, "%s was made by `host create`: %s. %s\n", m.h.Name, m.what(), m.holds())
	q := fmt.Sprintf("Type %s to delete the machine too, or press Enter to keep it:", m.h.Name)
	if o.destroy {
		q = fmt.Sprintf("Type %s to delete the machine, or press Enter to keep it:", m.h.Name)
	}
	ok, err := p.typedName(q, m.h.Name)
	switch {
	case err != nil:
		return false, err
	case !ok && o.destroy:
		return false, p.cancel()
	}
	return ok, nil
}

// destroy deletes the machine, and records it.
func (m *rmMachine) destroy(ctx context.Context, p *prompter, rec *client.Removal) error {
	dctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	err := m.lc.Destroy(dctx)
	cancel()
	if err != nil {
		return err
	}
	p.changes().Add("deleted %s", m.what())
	if err := rec.Record(client.RemovalDestroy); err != nil {
		return err
	}
	(&client.SSH{Host: m.h}).ExitMaster(ctx)
	fmt.Fprintf(p.out, "Deleted %s, with its disk and every session on it.\n", m.what())
	return nil
}
