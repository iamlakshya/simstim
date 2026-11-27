package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/iamlakshya/simstim/internal/keyread"
)

// This owner can execute only the local fixed undo template. A removal DTO,
// root-step history or hash cannot construct an execution or native source.
type purgeRootUndoExecutionV1 struct {
	mu        sync.Mutex
	request   *GrantRequest
	purge     *purgeOperationV1
	script    RootScript
	selection *purgeRootSelectionV1
	mode      RootStepMode
	yes       bool
	attempted bool
	busy      bool
	closed    bool
	process   *purgeRootUndoProcessV1
}

type purgeRootUndoProcessV1 struct {
	owner  *purgeRootUndoExecutionV1
	cmd    *exec.Cmd
	finish func() error
	once   sync.Once
	err    error
}

// PrintPurgeRootUndoReview seals and prints the SAME private fixed execution
// that Run consumes. It accepts no caller script or source/approval DTO.
func PrintPurgeRootUndoReview(ctx context.Context, mode RootStepMode, options RootRunOptions) error {
	p, err := purgeOperationOfV1(ctx)
	if err != nil {
		return err
	}
	if err := p.checkRootUndoSourceV1(ctx, p.conn, mode, options); err != nil {
		return err
	}
	p.mu.Lock()
	if p.busy {
		p.mu.Unlock()
		return agentModeResourceError()
	}
	p.busy = true
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.busy = false; p.mu.Unlock() }()
	execution, err := preparePurgeRootUndoExecutionV1(ctx, p, mode, options)
	if err != nil {
		return err
	}
	return printPurgeRootUndoExecutionV1(execution, options)
}

func preparePurgeRootUndoExecutionV1(ctx context.Context, p *purgeOperationV1, mode RootStepMode, options RootRunOptions) (*purgeRootUndoExecutionV1, error) {
	if mode == RootSkip {
		return nil, nil
	}
	if mode != RootPrint && mode != RootRun {
		return nil, grantPurposeError()
	}
	r := grantRequestOf(ctx)
	r.mu.Lock()
	execution := r.purgeUndo
	r.mu.Unlock()
	if execution != nil {
		if execution.request != r || execution.purge != p || execution.mode != mode || execution.yes != options.Passwordless || execution.selection == nil {
			return nil, grantOperationError()
		}
		if err := execution.selection.check(ctx, p); err != nil {
			return nil, err
		}
		return execution, nil
	}
	needed, err := hasPurgeRootUndoStepsV1(ctx, p)
	if err != nil {
		return nil, err
	}
	if !needed {
		return nil, nil
	}
	// Account identity is read through the SAME actual native continuation.
	// A report or local root history cannot choose the privileged account.
	identity, err := p.runFixedNative(ctx, "LC_ALL=C /usr/bin/id -un && /usr/bin/id -u", nil, 256)
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(string(identity))
	if len(fields) != 2 || !rootUserRE.MatchString(fields[0]) || fields[0] == "root" {
		return nil, rootStepError("the original purge has no valid non-root native account")
	}
	uid, err := strconv.Atoi(fields[1])
	if err != nil || uid <= 0 || strconv.Itoa(uid) != fields[1] {
		return nil, rootStepError("the original purge has no canonical native account uid")
	}
	selection, err := preparePurgeRootUndoSelectionV1(ctx, p, fields[0], uid)
	if err != nil {
		return nil, err
	}
	if selection.empty {
		return nil, nil
	}
	if err := p.bindRootUndoTemplateV1(ctx, p.conn, selection.script.SHA256); err != nil {
		return nil, err
	}
	execution = &purgeRootUndoExecutionV1{request: r, purge: p, script: selection.script, selection: selection, mode: mode, yes: options.Passwordless}
	r.mu.Lock()
	if r.purgeUndo != nil {
		r.mu.Unlock()
		return nil, grantOperationError()
	}
	r.purgeUndo = execution
	r.mu.Unlock()
	p.mu.Lock()
	p.rootUndoSelected = true
	p.mu.Unlock()
	return execution, nil
}

func printPurgeRootUndoExecutionV1(execution *purgeRootUndoExecutionV1, options RootRunOptions) error {
	if options.Out == nil {
		options.Out = os.Stderr
	}
	if execution == nil {
		_, err := fmt.Fprintln(options.Out, "No locally recorded root run needs undo for this host.")
		return err
	}
	_, err := fmt.Fprintf(options.Out, "%s\nSHA-256 %s\n", execution.script.Text, execution.script.SHA256)
	return err
}

// RunPurgeRootUndo consumes only this original request's sealed local plan.
// Print, skip and a known empty local history never acknowledge a root run.
func RunPurgeRootUndo(ctx context.Context, mode RootStepMode, options RootRunOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := purgeOperationOfV1(ctx)
	if err != nil {
		return err
	}
	if err := p.checkRootUndoSourceV1(ctx, p.conn, mode, options); err != nil {
		return err
	}
	if mode == RootSkip {
		return nil
	}
	p.mu.Lock()
	if p.busy {
		p.mu.Unlock()
		return agentModeResourceError()
	}
	p.busy = true
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.busy = false; p.mu.Unlock() }()
	execution, err := preparePurgeRootUndoExecutionV1(ctx, p, mode, options)
	if err != nil {
		return err
	}
	if err := printPurgeRootUndoExecutionV1(execution, options); err != nil {
		return err
	}
	if execution == nil || mode == RootPrint {
		return nil
	}
	if err := execution.selection.check(ctx, p); err != nil {
		return err
	}
	r := grantRequestOf(ctx)
	operation, err := r.borrowNewSettings(ctx)
	if err != nil {
		return err
	}
	settings, err := operation.Settings(ctx)
	if err != nil {
		return err
	}
	tier, hid, err := grantHostTier(ctx, p.receipt.Original)
	if err != nil {
		return err
	}
	if hid != p.receipt.HostID {
		return grantIdentityError(p.receipt.Original)
	}
	tier = min(tier, requestCeiling(ctx, tier))
	grant := Outbound{Kind: OutboundRootRun, Name: execution.script.SHA256}
	if err := allowRequestGrant(ctx, p.receipt.Original, hid, tier, grant, settings); err != nil {
		return err
	}
	if options.In == nil {
		options.In = os.Stdin
	}
	if options.Out == nil {
		options.Out = os.Stderr
	}
	if !options.Passwordless && (!InputAllowed(ctx) || !rootStepTerminal(options.In)) {
		return rootStepError("the purge undo needs its own visible terminal for sudo")
	}
	if err := keyread.Release(options.In); err != nil {
		return &UserError{Msg: "the terminal's previous reader did not stop", Hint: "Open a new terminal and resume the original purge.", Code: "terminal_busy", Kind: KindRefused}
	}
	marker := &rootMarkerWriter{out: options.Out}
	options.Out = marker
	process, err := execution.start(ctx, options, tier, grant)
	if err != nil {
		return err
	}
	err = waitRootStepProcess(ctx, process)
	if err := rootStepApplyResult(ctx, err, marker.done()); err != nil {
		return err
	}
	if err := execution.selection.check(ctx, p); err != nil {
		return err
	}
	if err := p.checkRootUndoSourceV1(ctx, p.conn, mode, options); err != nil {
		return err
	}
	p.mu.Lock()
	p.rootUndoRan = true
	p.mu.Unlock()
	return nil
}

func (e *purgeRootUndoExecutionV1) start(ctx context.Context, options RootRunOptions, tier Tier, grant Outbound) (_ *purgeRootUndoProcessV1, returned error) {
	p := e.purge
	transport, owned, err := p.fixedTransport(ctx)
	if err != nil {
		return nil, err
	}
	ssh := *transport
	ssh.Interactive = !options.Passwordless
	native, finish, err := ssh.beginNativeSource(owned)
	if err != nil {
		return nil, err
	}
	retained := false
	defer func() {
		if !retained {
			returned = errors.Join(returned, finish())
		}
	}()
	locked, release, err := LockHost(native, p.receipt.Original.Name, "start original fixed purge undo")
	if err != nil {
		return nil, err
	}
	defer release()
	if err := p.checkRootUndoSourceV1(locked, p.conn, e.mode, options); err != nil {
		return nil, err
	}
	if e.selection == nil {
		return nil, grantOperationError()
	}
	if err := e.selection.check(locked, p); err != nil {
		return nil, err
	}
	if err := requirePurgeRootOutboundV1(locked, e.request, grant); err != nil {
		return nil, err
	}
	operation, err := e.request.borrowNewSettings(locked)
	if err != nil {
		return nil, err
	}
	settings, err := operation.Settings(locked)
	if err != nil {
		return nil, err
	}
	current, hid, err := grantHostTier(locked, p.receipt.Original)
	if err != nil {
		return nil, err
	}
	if hid != p.receipt.HostID {
		return nil, grantIdentityError(p.receipt.Original)
	}
	tier = min(tier, current, requestCeiling(locked, current))
	if err := allowRequestGrant(locked, p.receipt.Original, hid, tier, grant, settings); err != nil {
		return nil, err
	}
	e.mu.Lock()
	if e.closed || e.busy || e.attempted {
		e.mu.Unlock()
		return nil, grantOperationError()
	}
	e.busy, e.attempted = true, true
	e.mu.Unlock()
	started := false
	defer func() {
		if !started {
			e.mu.Lock()
			e.busy = false
			e.mu.Unlock()
		}
	}()
	if err := recordRequestGrant(locked, p.receipt.Original, hid, tier, grant); err != nil {
		return nil, err
	}
	remote := "/usr/bin/sudo "
	terminal := []string{"-t"}
	if options.Passwordless {
		remote += "-n "
		terminal = []string{"-t", "-t"}
	}
	remote += "/bin/sh -c " + ShellQuote(e.script.Text)
	command, err := ssh.command(locked, native, remote, terminal...)
	if err != nil {
		return nil, err
	}
	command.Stdin, command.Stdout, command.Stderr = options.In, options.Out, options.Out
	command.WaitDelay = sshWaitDelay
	if err := operation.Revalidate(locked); err != nil {
		return nil, err
	}
	if err := p.checkRootUndoSourceV1(locked, p.conn, e.mode, options); err != nil {
		return nil, err
	}
	if err := e.selection.check(locked, p); err != nil {
		return nil, err
	}
	if err := ssh.checkNativeSourceScope(locked); err != nil {
		return nil, err
	}
	if err := p.markRootDispatchV1(locked); err != nil {
		return nil, err
	}
	if err := e.selection.check(locked, p); err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, err
	}
	process := &purgeRootUndoProcessV1{owner: e, cmd: command, finish: finish}
	e.mu.Lock()
	e.process = process
	e.mu.Unlock()
	retained, started = true, true
	return process, nil
}

func (p *purgeRootUndoProcessV1) Wait() error {
	p.once.Do(func() {
		p.err = errors.Join(p.cmd.Wait(), p.finish())
		p.owner.mu.Lock()
		p.owner.process, p.owner.busy = nil, false
		p.owner.mu.Unlock()
	})
	return p.err
}

func (p *purgeRootUndoProcessV1) Kill() {
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

func (e *purgeRootUndoExecutionV1) close() error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	if e.busy || e.process != nil {
		return agentModeResourceError()
	}
	return nil
}

func requirePurgeRootOutboundV1(ctx context.Context, r *GrantRequest, grant Outbound) error {
	if r == nil || r != grantRequestOf(ctx) || grant.Kind != OutboundRootRun {
		return grantPurposeError()
	}
	r.mu.Lock()
	e, p := r.purgeUndo, r.purge
	valid := r.purpose == grantPurge && r.operation == grantOpPurge && !r.Untrusted && !r.SSHAgent && r.lifetimeErrorLocked() == nil
	r.mu.Unlock()
	if !valid || e == nil || p == nil || e.request != r || e.purge != p || e.mode != RootRun || grant.Name != e.script.SHA256 {
		return grantPurposeError()
	}
	p.mu.Lock()
	valid = p.rootUndoSHA == grant.Name && p.rootMode == RootRun && p.rootConfigured
	p.mu.Unlock()
	if !valid {
		return grantPurposeError()
	}
	if e.selection == nil {
		return grantPurposeError()
	}
	if err := e.selection.check(ctx, p); err != nil {
		return err
	}
	return p.checkRootUndoSourceV1(ctx, p.conn, e.mode, RootRunOptions{Passwordless: e.yes})
}
