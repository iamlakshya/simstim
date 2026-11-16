package client

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"slices"
	"sync"
	"time"

	"golang.org/x/term"
)

type rootStepPhase uint8

const (
	rootStepPasswordlessProbe rootStepPhase = iota + 1
	rootStepApply
	rootStepUserManagerReexec
)

type rootStepOwnedProcess interface {
	Wait() error
	Kill()
}
type rootStepExecution struct {
	mu                      sync.Mutex
	intent                  *rootStepIntent
	request                 *GrantRequest
	probeStarted, probeDone bool
	applyStarted, applyDone bool
	reexecStarted           bool
	attempted, starting     bool
	done, verified, closed  bool
	startedSeen             bool
	process                 *rootStepProcess
}

type rootStepProcess struct {
	execution    *rootStepExecution
	phase        rootStepPhase
	command      *exec.Cmd
	finishNative func() error
	once         sync.Once
	err          error
}

// RunRootStep owns the exact fixed script, actual native process and final
// fresh effect check. The terminal helper receives only its closed execution.
func (c *Conn) RunRootStep(ctx context.Context, script RootScript, originalMode RootStepMode, options RootRunOptions) (returned error) {
	intent, err := requireRootIntent(ctx, c, script, originalMode)
	if err != nil {
		return err
	}
	if originalMode != RootRun || options.Passwordless != intent.yes {
		return grantPurposeError()
	}
	if options.In == nil {
		options.In = os.Stdin
	}
	if options.Out == nil {
		options.Out = os.Stderr
	}
	r := grantRequestOf(ctx)
	r.mu.Lock()
	if r.rootExecution != nil {
		r.mu.Unlock()
		return grantOperationError()
	}
	execution := &rootStepExecution{intent: intent, request: r}
	r.rootExecution = execution
	r.mu.Unlock()
	err = runRootStepExecution(ctx, execution, options)
	if err == nil && slices.ContainsFunc(intent.script.Steps, func(step RootStep) bool { return step.ID == RootDelegateIO }) {
		process, startErr := execution.start(ctx, rootStepUserManagerReexec, options)
		err = startErr
		if err == nil {
			err = waitRootStepProcess(ctx, process)
		}
	}
	execution.mu.Lock()
	execution.done = true
	attempted := execution.attempted
	execution.mu.Unlock()
	if err == nil {
		after, probeErr := c.CapProbe(ctx, "")
		if probeErr == nil {
			probeErr = bindRequestTransportSource(ctx, c)
		}
		if probeErr == nil {
			probeErr = VerifyRootSteps(intent.before, after, intent.script)
		}
		err = probeErr
		if err == nil {
			execution.mu.Lock()
			execution.verified = true
			execution.mu.Unlock()
		}
	}
	if attempted {
		status := "failed"
		if err == nil {
			status = "confirmed"
		}
		err = errors.Join(err, execution.record(ctx, status))
	}
	return err
}

func (e *rootStepExecution) start(ctx context.Context, phase rootStepPhase, options RootRunOptions) (_ rootStepOwnedProcess, returned error) {
	if e == nil || e.intent == nil || options.Passwordless != e.intent.yes {
		return nil, grantPurposeError()
	}
	if _, err := requireRootIntent(ctx, e.intent.conn, e.intent.script, RootRun); err != nil {
		return nil, err
	}
	if !options.Passwordless && (options.In == nil || !InputAllowed(ctx) || !rootStepTerminal(options.In)) {
		return nil, rootStepError("interactive root execution needs its own terminal")
	}
	probe, err := e.intent.conn.CapProbe(ctx, "")
	if err != nil {
		return nil, err
	}
	if _, err := canonicalRootScript(probe, e.intent.script); err != nil {
		return nil, err
	}
	localStarted, err := RootHistoryStarted(ctx, e.intent.conn.Host, e.intent.conn.HostID())
	if err != nil {
		return nil, err
	}
	ssh, ok := e.intent.conn.route.t.(*SSH)
	if !ok || ssh.Host.SSH == "" {
		return nil, rootStepError("the root step needs its original SSH route")
	}
	transport := *ssh
	transport.Interactive = !options.Passwordless
	native, finishNative, err := transport.beginNativeSource(ctx)
	if err != nil {
		return nil, err
	}
	ownedNative := true
	defer func() {
		if ownedNative {
			returned = errors.Join(returned, finishNative())
		}
	}()
	locked, release, err := e.intent.conn.beginLocalGrant(native, grantLocalRoot, grantOpRootStep, "start exact root step")
	if err != nil {
		return nil, err
	}
	defer release()
	if err := e.intent.source.check(locked, e.intent.conn); err != nil {
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
	tier, hid, err := grantHostTier(locked, e.intent.conn.Host)
	if err != nil {
		return nil, err
	}
	if hid != e.intent.conn.HostID() {
		return nil, grantIdentityError(e.intent.conn.Host)
	}
	tier = min(tier, requestCeiling(locked, tier))
	grant := Outbound{Kind: OutboundRootRun, Name: e.intent.script.SHA256}
	if err := allowRequestGrant(locked, e.intent.conn.Host, hid, tier, grant, settings); err != nil {
		return nil, err
	}
	e.mu.Lock()
	if e.closed || e.done || e.process != nil || phase == rootStepPasswordlessProbe && (!e.intent.yes || e.probeStarted) ||
		phase == rootStepApply && (e.applyStarted || e.intent.yes && !e.probeDone) ||
		phase == rootStepUserManagerReexec && (!e.applyDone || e.reexecStarted || !slices.ContainsFunc(e.intent.script.Steps, func(step RootStep) bool { return step.ID == RootDelegateIO })) ||
		phase != rootStepPasswordlessProbe && phase != rootStepApply && phase != rootStepUserManagerReexec {
		e.mu.Unlock()
		return nil, grantOperationError()
	}
	switch phase {
	case rootStepPasswordlessProbe:
		e.probeStarted = true
	case rootStepApply:
		e.applyStarted = true
	case rootStepUserManagerReexec:
		e.reexecStarted = true
	}
	e.attempted = true
	e.starting = true
	// Preserve every actually observed used-account fact in this attempt.
	// Later session removal cannot turn its history back into zero.
	e.startedSeen = e.startedSeen || localStarted || probe.Started != 0
	e.mu.Unlock()
	defer func() { e.mu.Lock(); e.starting = false; e.mu.Unlock() }()
	if err := recordRequestGrant(locked, e.intent.conn.Host, hid, tier, grant); err != nil {
		return nil, err
	}
	if err := e.record(locked, "running"); err != nil {
		return nil, err
	}
	remote := "/usr/bin/sudo -n true"
	terminal := []string{"-T"}
	if phase == rootStepApply {
		remote = "/usr/bin/sudo "
		if options.Passwordless {
			remote += "-n "
			terminal = []string{"-t", "-t"}
		} else {
			terminal = []string{"-t"}
		}
		remote += "/bin/sh -c " + ShellQuote(e.intent.script.Text)
	} else if phase == rootStepUserManagerReexec {
		remote = "/usr/bin/systemctl --user daemon-reexec"
	}
	command, err := transport.command(locked, native, remote, terminal...)
	if err != nil {
		return nil, err
	}
	if phase == rootStepApply {
		command.Stdin = options.In
		command.Stdout = options.Out
		command.Stderr = options.Out
	} else {
		command.Stdout = io.Discard
		command.Stderr = io.Discard
	}
	command.WaitDelay = sshWaitDelay
	if err := operation.Revalidate(locked); err != nil {
		return nil, err
	}
	if err := e.intent.source.check(locked, e.intent.conn); err != nil {
		return nil, err
	}
	if err := transport.checkNativeSourceScope(locked); err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, err
	}
	process := &rootStepProcess{execution: e, phase: phase, command: command, finishNative: finishNative}
	e.mu.Lock()
	e.process = process
	e.mu.Unlock()
	ownedNative = false
	release()
	return process, nil
}

func (p *rootStepProcess) Wait() error {
	p.once.Do(func() {
		p.err = p.command.Wait()
		p.err = errors.Join(p.err, p.finishNative())
		p.execution.mu.Lock()
		if p.err == nil {
			if p.phase == rootStepPasswordlessProbe {
				p.execution.probeDone = true
			} else if p.phase == rootStepApply {
				p.execution.applyDone = true
			}
		}
		p.execution.process = nil
		p.execution.mu.Unlock()
	})
	return p.err
}
func (p *rootStepProcess) Kill() {
	if p.command.Process != nil {
		_ = p.command.Process.Kill()
	}
}
func (e *rootStepExecution) close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	if e.process != nil || e.starting {
		return agentModeResourceError()
	}
	return nil
}
func (e *rootStepExecution) record(ctx context.Context, status string) error {
	ids := make([]RootStepID, 0, len(e.intent.script.Steps))
	for _, step := range e.intent.script.Steps {
		ids = append(ids, step.ID)
	}
	e.mu.Lock()
	startedSeen := e.startedSeen
	e.mu.Unlock()
	bound := context.WithValue(ctx, rootStepRecordProofKey{}, e)
	return RecordRootStep(bound, e.intent.conn.Host, RootStepRecord{HostID: e.intent.conn.HostID(), Steps: ids, SHA256: e.intent.script.SHA256,
		Mode: RootRun, At: time.Now().UTC(), Status: status, StartedSeen: startedSeen})
}

func rootStepTerminal(file *os.File) bool {
	if file == nil {
		return false
	}
	raw, err := file.SyscallConn()
	if err != nil {
		return false
	}
	terminal := false
	if err := raw.Control(func(fd uintptr) { terminal = term.IsTerminal(int(fd)) }); err != nil {
		return false
	}
	return terminal
}
