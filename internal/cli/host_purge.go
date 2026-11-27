package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func removePurgedHost(ctx context.Context, p *prompter, o rmOpts) (returned error) {
	h, err := client.FindHost(o.name)
	if err != nil {
		var unknown *client.UnknownNameError
		if errors.As(err, &unknown) {
			if o.confirmed && o.confirm != o.name {
				return &client.UserError{Msg: "--confirm differs from the original host name", Hint: o.line(), Code: "confirm_mismatch", Kind: client.KindUsage}
			}
			found, retirementErr := client.FinishPurgedHostRetirement(ctx, o.name)
			if retirementErr != nil {
				return retirementErr
			}
			if found {
				if o.asJSON {
					return printJSON(hostRemovalJSONV1{Host: client.CleanLine(o.name), Removed: true, LocalCleanup: true})
				}
				fmt.Fprintf(p.out, "Finished the local removal record of %s.\n", o.name)
				return nil
			}
		}
		return unknownToRm(err, o)
	}
	if o.confirmed && o.confirm != h.Name {
		return &client.UserError{Msg: fmt.Sprintf("--confirm got %q, which is not the name of the host %s", o.confirm, h.Name), Hint: o.line(), Code: "confirm_mismatch", Kind: client.KindUsage}
	}
	if o.destroy && h.Machine == nil {
		return &client.UserError{Msg: h.Name + " was not made by host create, so there is no machine to delete", Hint: rmOpts{name: h.Name, purge: true}.line(), Code: "not_created", Kind: client.KindUsage}
	}
	if !o.confirmed && !p.asks(qRmPurge) {
		return &client.UserError{Msg: "host rm --purge needs the typed name of " + h.Name + ", and input is off", Hint: o.line("--confirm", shellWord(h.Name)), Code: "needs_answer", Kind: client.KindNeedsAnswer}
	}
	var mode client.RootStepMode
	if o.rootStepSet || o.rootStep != "" {
		mode, err = client.ParseRootStepMode(o.rootStep)
		if err != nil {
			return err
		}
	}
	passwordless := mode == client.RootRun && !p.on
	beforeHostLock(h.Name)
	ctx, unlock, err := client.LockHost(ctx, h.Name, "host rm --purge")
	if err != nil {
		return err
	}
	defer unlock()
	if err := sameHost(h, o); err != nil {
		return err
	}
	request := client.NewPurgeGrantRequest()
	defer func() { returned = errors.Join(returned, request.Close()) }()
	ctx = client.WithGrantRequest(client.WithoutWake(client.WithoutInstall(ctx)), request)
	resumed, err := client.ResumeHostPurge(ctx, h, o.force, mode, passwordless)
	if err != nil {
		return err
	}
	var conn *client.Conn
	var macLeaveNotice string
	var sessions []client.Session
	if resumed {
		mode, passwordless, err = client.PurgeRootDecision(ctx)
		if err != nil {
			return err
		}
	}
	if !resumed {
		conn, err = client.Dial(ctx, h, p.on)
		if err != nil {
			return err
		}
		defer conn.Close()
		macLeaveNotice, _ = conn.MacLeaveNoticeV1(ctx)
		if mode == "" {
			probe, err := conn.CapProbe(ctx, "")
			if err != nil {
				return err
			}
			tier, hostID, err := client.HostGrantTier(ctx, h)
			if err != nil {
				return err
			}
			if hostID != conn.HostID() {
				return &client.UserError{Msg: "the host identity changed before choosing its root undo", Hint: o.line(), Code: "host_changed", Kind: client.KindRefused}
			}
			started, err := client.RootHistoryStarted(ctx, h, hostID)
			if err != nil {
				return err
			}
			mode = client.DefaultRootStepMode(tier, started, probe, !p.on)
			passwordless = mode == client.RootRun && !p.on
		}
		sessions, err = conn.Sessions(ctx)
		if err != nil && !o.force {
			return &client.UserError{Msg: "the sessions on " + h.Name + " could not be checked, so its removal stopped", Hint: o.line() + " (after checking the host)", Code: "working", Kind: client.KindRefused}
		}
		if !o.force {
			for _, session := range sessions {
				if session.State == api.StateWorking || session.State == api.StateStarting {
					return &client.UserError{Msg: "a session on " + h.Name + " is working: " + shown(session.Name), Hint: o.line("--force"), Code: "working", Kind: client.KindRefused}
				}
			}
		}
	}
	if !o.confirmed {
		text := fmt.Sprintf("This stops %d sessions. Type %s to remove everything %s put on it, or press Enter to keep it:", len(sessions), h.Name, brand.Name)
		if resumed {
			text = fmt.Sprintf("Type %s to finish removing everything %s put on it, or press Enter to keep it:", h.Name, brand.Name)
		}
		yes, err := p.typedName(text, h.Name)
		if err != nil {
			return err
		}
		if !yes {
			return cancelled()
		}
	}
	if err := sameHost(h, o); err != nil {
		return err
	}
	if resumed {
		report, err := client.ContinuePurgeHostStage(ctx)
		printPurgeReportV1(p, report)
		if err != nil {
			return err
		}
	}
	if conn != nil {
		if _, err := conn.PreparePurge(ctx, o.force, mode, passwordless); err != nil {
			return err
		}
		report, err := conn.Purge(ctx, o.force)
		printPurgeReportV1(p, report)
		if err != nil {
			return err
		}
	}
	if macLeaveNotice != "" {
		fmt.Fprintln(p.out, macLeaveNotice)
	}
	p.changes().Add("completed the recorded host cleanup phase on %s", h.Name)
	var rootOutput purgeRootOutputV1
	rootWriter := p.out
	if o.asJSON {
		rootWriter = io.MultiWriter(&rootOutput, stderrFor(true, "root_undo"))
	}
	rootOptions := client.RootRunOptions{Passwordless: passwordless, In: os.Stdin, Out: rootWriter}
	if mode == client.RootRun && !o.rootStepSet {
		needed, err := client.PurgeHasRootUndoSteps(ctx)
		if err != nil {
			return err
		}
		if needed {
			if err := client.PrintPurgeRootUndoReview(ctx, mode, rootOptions); err != nil {
				return err
			}
			yes, err := p.confirm(qPurgeRootStep, "Run this fixed root undo with sudo on "+client.CleanLine(h.Name)+"?", "a yes to run the root undo")
			if err != nil {
				return err
			}
			if !yes {
				return p.cancel()
			}
		}
	}
	ctx, err = purgeGrantQuestionsV1(ctx, p, h.Name, func(current context.Context) error { return client.FinishPurgeRootUndo(current, mode, rootOptions) })
	if err != nil {
		return err
	}
	logins, err := client.PurgeLoginFiles(ctx)
	if err != nil {
		return err
	}
	removeLogins := o.deleteLogins
	if len(logins) != 0 {
		fmt.Fprintln(p.out, "Agent login files:")
		for _, path := range logins {
			fmt.Fprintln(p.out, "  "+shown(path))
		}
		if !removeLogins && p.on {
			removeLogins, err = p.confirm(qRmLogins, "Delete these logins on "+h.Name+"?", "a yes to delete agent login files")
			if err != nil {
				return err
			}
		}
	}
	if err := client.PurgeAgentLogins(ctx, removeLogins); err != nil {
		return err
	}
	changes, err := client.CheckPurgeBaseline(ctx)
	if err != nil {
		return err
	}
	if len(changes.Changed) != 0 {
		fmt.Fprintf(p.out, "Changed since the host was added: %s. Inspect these changes by hand.\n", strings.Join(changes.Changed, ", "))
	}
	if len(changes.Unavailable) != 0 {
		fmt.Fprintf(p.out, "Could not compare: %s.\n", strings.Join(changes.Unavailable, ", "))
	}
	names, err := client.PurgeRotateNotice(ctx)
	if err != nil {
		return err
	}
	if line := client.RotateReminder(h, names); line != "" {
		fmt.Fprintln(p.out, line)
	}
	fmt.Fprintln(p.out, "The host's SSH key lines stay in authorised_keys. Remove the key on the host if it is no longer needed.")
	if err := client.CommitHostPurge(ctx); err != nil {
		return err
	}
	if o.destroy {
		if err := client.DestroyPurgedHost(ctx); err != nil {
			return err
		}
	}
	var public *hostPurgeJSONV1
	if o.asJSON {
		report, err := client.PurgeOutcomeReport(ctx)
		if err != nil {
			return err
		}
		public = publicPurgeReportV1(report)
		public.MacLeaveNotice = macLeaveNotice
		public.BaselineChanged = publicPurgeStringsV1(changes.Changed)
		public.BaselineUnavailable = publicPurgeStringsV1(changes.Unavailable)
		public.Rotate = publicPurgeStringsV1(names)
		public.RootMode = string(mode)
		public.RootUndoRan, err = client.PurgeRootExecution(ctx)
		if err != nil {
			return err
		}
		public.RootOutput = rootOutput.String()
	}
	if err := client.ForgetPurgedHost(ctx); err != nil {
		return err
	}
	if o.asJSON {
		return printJSON(hostRemovalJSONV1{Host: client.CleanLine(h.Name), Removed: true, Destroyed: o.destroy, Purge: public})
	}
	fmt.Fprintf(p.out, "Removed host resources and forgot %s.\n", h.Name)
	return nil
}

func printPurgeReportV1(p *prompter, report api.PurgeResult) {
	for _, item := range report.Removed {
		fmt.Fprintf(p.out, "Removed %s: %s.\n", shown(item.Kind), shown(item.Path))
	}
	for _, item := range report.Left {
		fmt.Fprintf(p.out, "Kept %s: %s. %s\n", shown(item.Kind), shown(item.Path), shown(item.Reason))
		if item.Hint != "" {
			fmt.Fprintln(p.out, "Try: "+shown(item.Hint))
		}
	}
	for _, session := range report.Unsaved {
		fmt.Fprintf(p.out, "Unsaved session %s: %s.\n", shown(session.Name), shown(session.Reason))
	}
}

func purgeGrantQuestionsV1(ctx context.Context, p *prompter, host string, run func(context.Context) error) (context.Context, error) {
	for {
		err := run(ctx)
		var question *client.ConfirmError
		if !errors.As(err, &question) {
			return ctx, err
		}
		if !p.on {
			return ctx, err
		}
		if question.Prompt == "" || question.Host != host || !client.ValidHostName(question.Host) {
			return ctx, &client.UserError{Msg: "the root grant question does not match the original host", Hint: "Keep the host saved and inspect the original root undo before retrying.", Code: "grant_confirmation", Kind: client.KindRefused}
		}
		var yes bool
		var answerErr error
		switch question.Kind {
		case client.ConfirmYesNo:
			yes, answerErr = p.confirm(qPurgeRootGrant, client.CleanLine(question.Prompt), "a live terminal answer for this root grant")
		case client.ConfirmHostName:
			yes, answerErr = p.typedName(client.CleanLine(question.Prompt), question.Host)
		default:
			return ctx, &client.UserError{Msg: "the root grant question has an unknown answer type", Hint: "Run the same removal with a matching current client.", Code: "grant_confirmation", Kind: client.KindRefused}
		}
		if answerErr != nil {
			return ctx, answerErr
		}
		if !yes {
			return ctx, p.cancel()
		}
		if err := ctx.Err(); err != nil {
			return ctx, err
		}
		ctx, answerErr = client.ConfirmGrant(ctx, err)
		if answerErr != nil {
			return ctx, answerErr
		}
	}
}
