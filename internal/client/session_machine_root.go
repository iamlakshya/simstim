package client

import (
	"context"
	"errors"
	"io"
	"strings"
)

// This exception is owned by the actual creation, never by machine metadata,
// --yes, a script hash or a saved preparation receipt.
func (o *SessionMachineOwnerV1) checkRootV1(ctx context.Context, c *Conn, mode RootStepMode, passwordless bool) error {
	if o == nil || o.closed.Load() || o.committed.Load() || o.resource == nil || mode != RootRun || !passwordless || c == nil || c.route.machineOwner != o || o.capture.target == nil {
		return sessionMachineErrorV1("the passwordless root step has no original new-machine owner")
	}
	if err := o.capture.target.checkConn(ctx, c); err != nil {
		return err
	}
	original := o.resource.Host()
	if original.Machine == nil || c.Host.Machine == nil || original.Name != c.Host.Name || original.Machine.Provider != "e2b" || original.Machine.ID != c.Host.Machine.ID {
		return grantIdentityError(c.Host)
	}
	return nil
}

func (o *SessionMachineOwnerV1) runRootV1(ctx context.Context, intent *rootStepIntent, options RootRunOptions) (returned error) {
	c := intent.conn
	if err := o.checkRootV1(ctx, c, RootRun, options.Passwordless); err != nil {
		return err
	}
	probe, err := c.CapProbe(ctx, "")
	if err != nil {
		return err
	}
	if !probe.Sudo || !probe.SudoPasswordless {
		return rootStepError("the new machine does not allow sudo without a password")
	}
	if _, err := canonicalRootScript(probe, intent.script); err != nil {
		return err
	}
	request := grantRequestOf(ctx)
	operation, err := request.borrowNewSettings(ctx)
	if err != nil {
		return err
	}
	if err := operation.Revalidate(ctx); err != nil {
		return err
	}
	owned, release, err := c.beginLocalGrant(ctx, grantLocalRoot, grantOpRootStep, "run the new machine's exact passwordless root step")
	if err != nil {
		return err
	}
	defer release()
	if err := intent.source.check(owned, c); err != nil {
		return err
	}
	if err := o.checkRootV1(owned, c, RootRun, true); err != nil {
		return err
	}
	request.mu.Lock()
	if request.rootExecution != nil {
		request.mu.Unlock()
		return grantOperationError()
	}
	execution := &rootStepExecution{intent: intent, request: request, attempted: true, starting: true, startedSeen: probe.Started != 0}
	request.rootExecution = execution
	request.mu.Unlock()
	defer func() { execution.mu.Lock(); execution.starting = false; execution.done = true; execution.mu.Unlock() }()
	if err := recordRequestGrant(owned, c.Host, c.HostID(), TierShared, Outbound{Kind: OutboundRootRun, Name: intent.script.SHA256}); err != nil {
		return err
	}
	if err := execution.record(owned, "running"); err != nil {
		return err
	}
	if err := operation.Revalidate(owned); err != nil {
		return err
	}
	if err := intent.source.check(owned, c); err != nil {
		return err
	}
	process, err := c.route.t.Start(owned, "sudo -n /bin/sh -s")
	release()
	if err == nil {
		err = runSessionMachineRootProcessV1(ctx, process, intent.script.Text, options.Out)
	}
	execution.mu.Lock()
	execution.starting, execution.done = false, true
	execution.mu.Unlock()
	if err == nil {
		after, probeErr := c.CapProbe(ctx, "")
		if probeErr == nil {
			probeErr = o.checkRootV1(ctx, c, RootRun, true)
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
	status := "failed"
	if err == nil {
		status = "confirmed"
	}
	return errors.Join(err, execution.record(ctx, status))
}

func runSessionMachineRootProcessV1(ctx context.Context, process Proc, script string, output io.Writer) error {
	out := output
	if out == nil {
		out = io.Discard
	}
	seen := &rootMarkerWriter{out: out}
	writeDone, readDone, waitDone := make(chan error, 1), make(chan error, 1), make(chan error, 1)
	go func() {
		_, writeErr := io.Copy(process.Stdin(), strings.NewReader(script))
		writeErr = errors.Join(writeErr, process.Stdin().Close())
		if writeErr != nil {
			process.Kill()
		}
		writeDone <- writeErr
	}()
	go func() {
		_, readErr := io.Copy(seen, process.Stdout())
		if readErr != nil {
			process.Kill()
		}
		readDone <- readErr
	}()
	go func() { waitDone <- process.Wait() }()
	var err error
	select {
	case err = <-waitDone:
	case <-ctx.Done():
		process.Kill()
		err = errors.Join(ctx.Err(), <-waitDone)
	}
	err = errors.Join(err, <-writeDone, <-readDone)
	return rootStepApplyResult(ctx, err, seen.done())
}
