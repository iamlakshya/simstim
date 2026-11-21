//go:build linux

package jail

import (
	"context"
	"errors"
	"net"

	"github.com/iamlakshya/simstim/internal/proto"

	"github.com/iamlakshya/simstim/internal/supply"
)

// SettleVMMUnstartedV1 permanently closes this original unstarted Cmd before
// issuing its private no-creation receipt. Future Start is then impossible.
// An already-created or ambiguous command cannot take this path.
func (c *Cmd) SettleVMMUnstartedV1(source *supply.ExecModeLeaseV1) error {
	owner, err := c.vmmOwnerV1(source)
	if err != nil {
		return err
	}
	c.mu.Lock()
	already := c.phase == phaseFailed && !c.owned && c.cmd != nil && c.cmd.Process == nil
	if already {
		c.mu.Unlock()
		owner.mu.Lock()
		proved := owner.noCreation && owner.noFuture
		owner.mu.Unlock()
		if !proved {
			return vmmModeFailureV1("the failed command has no original no-creation proof")
		}
		if err := c.FinishVMMExecModeV1(context.WithoutCancel(c.ctx), source); err != nil {
			return err
		}
		c.mu.Lock()
		c.physical = true
		c.mu.Unlock()
		return nil
	}
	if c.phase != phaseNew || c.owned || c.cmd == nil || c.cmd.Process != nil {
		c.mu.Unlock()
		return vmmModeFailureV1("the VMM creation may already exist; retain its original owner")
	}
	c.phase = phaseFailed
	c.mu.Unlock()
	owner.mu.Lock()
	owner.noCreation, owner.noFuture = true, true
	owner.mu.Unlock()
	if err := c.FinishVMMExecModeV1(context.WithoutCancel(c.ctx), source); err != nil {
		return err
	}
	c.mu.Lock()
	c.physical = true
	c.mu.Unlock()
	return nil
}

// WaitVMMNoFutureExecV1 uses original native handles. In particular, death of
// the actual namespace PID1 closes every possible descendant in that namespace.
// A direct outer process's death or an ordinary EOF cannot substitute for it.
func (c *Cmd) WaitVMMNoFutureExecV1(ctx context.Context, source *supply.ExecModeLeaseV1) error {
	owner, err := c.vmmOwnerV1(source)
	if err != nil {
		return err
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.finalProved || owner.noFuture {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if owner.noCreation {
		owner.noFuture = true
		return nil
	}
	if owner.root == nil {
		return vmmModeFailureV1("the original creation lifetime is still unknown")
	}
	ready, err := ownedPIDFDReady(int(owner.root.process.Fd()), 0)
	if err != nil || !ready {
		return errors.Join(err, vmmModeFailureV1("the original VMM creation can still execute"))
	}
	namespaceInit := owner.init
	if owner.rootNamespace && owner.namespaceRootProved {
		namespaceInit = owner.root
	}
	if namespaceInit == nil && owner.readonly && owner.readonlyGroup != nil {
		if err := owner.readonlyGroup.closedExceptCreatorV1(); err != nil {
			return err
		}
		owner.noFuture, owner.readonlyPhysicalClosed = true, true
		return nil
	}
	if namespaceInit == nil {
		return vmmModeFailureV1("the original namespace creation lifetime is still unknown")
	}
	ready, err = ownedPIDFDReady(int(namespaceInit.process.Fd()), 0)
	if err != nil || !ready {
		return errors.Join(err, vmmModeFailureV1("the original VMM namespace can still contain a future exec"))
	}
	owner.noFuture = true
	return nil
}
func (c *Cmd) vmmNoCreationV1() error {
	owner := c.modeExecV1
	if owner == nil {
		return nil
	}
	c.mu.Lock()
	none := !c.owned && c.cmd != nil && c.cmd.Process == nil
	c.mu.Unlock()
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if !none || owner.root != nil {
		return vmmModeFailureV1("an ambiguous VMM creation cannot become a no-child receipt")
	}
	owner.noCreation = true
	return nil
}
func (c *Cmd) settleVMMNoFutureBeforeWaitV1() error {
	owner := c.modeExecV1
	if owner == nil {
		return nil
	}
	ctx := context.WithoutCancel(c.ctx)
	// A successful external exec ends only the artifact mode fence. It does
	// not prove physical closure of the original runtime or namespace init.
	if err := owner.checkPhysicalClosedV1(); err != nil {
		return err
	}
	if err := c.WaitVMMNoFutureExecV1(ctx, owner.source); err != nil {
		return err
	}
	if err := c.FinishVMMExecModeV1(ctx, owner.source); err != nil {
		return err
	}
	if err := owner.closeReadonlyLifetimeV1(); err != nil {
		return err
	}
	return owner.closeNativeActorsV1()
}

// FinishVMMExecModeV1 restores and releases the actual mode fence at a proven
// final boundary. Native task handles remain for the separate control/VM owner.
func (c *Cmd) FinishVMMExecModeV1(ctx context.Context, source *supply.ExecModeLeaseV1) error {
	owner, err := c.vmmOwnerV1(source)
	if err != nil {
		return err
	}
	owner.mu.Lock()
	proved := owner.finalProved || owner.noFuture
	released := owner.released
	owner.mu.Unlock()
	if released {
		return nil
	}
	if !proved {
		return vmmModeFailureV1("the original source has no native final or no-future proof")
	}
	if err := source.RestoreFinalV1(ctx); err != nil {
		return err
	}
	return owner.releaseV1()
}

// CheckVMMNativeSenderV1 is a purpose-private mapped-UID receive gate. It
// checks the actual still-live creation actor, never a version or caller flag.
func (c *Cmd) CheckVMMNativeSenderV1(source *supply.ExecModeLeaseV1, conn *net.UnixConn, credentials *proto.UnixCredentialsV1) error {
	owner, err := c.vmmOwnerV1(source)
	if err != nil {
		return err
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.control != conn || owner.runner == nil || credentials == nil || credentials.PID != owner.runner.pid || credentials.UID != owner.subUID || credentials.GID != owner.subGID {
		return vmmModeFailureV1("the mapped control sender is not this original runner")
	}
	return owner.runner.live()
}

// checkPhysicalClosedV1 is independent of finalProved/released/noFuture. Cmd
// must retain every original native actor until these actual creation handles
// report exit, including an outer that ends before its namespace PID1.
func (owner *vmmExecOwnerV1) checkPhysicalClosedV1() error {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.readonlyPhysicalClosed {
		return nil
	}
	if owner.noCreation {
		return nil
	}
	if owner.root == nil {
		return vmmModeFailureV1("the original physical VMM creation is still unknown")
	}
	ready, err := ownedPIDFDReady(int(owner.root.process.Fd()), 0)
	if err != nil || !ready {
		return errors.Join(err, vmmModeFailureV1("the original VMM root creation has not physically exited"))
	}
	namespaceInit := owner.init
	if owner.rootNamespace && owner.namespaceRootProved {
		namespaceInit = owner.root
	}
	if namespaceInit == nil && owner.readonly && owner.readonlyGroup != nil {
		if err := owner.readonlyGroup.closedExceptCreatorV1(); err != nil {
			return err
		}
		owner.readonlyPhysicalClosed = true
		return nil
	}
	if namespaceInit == nil {
		return vmmModeFailureV1("the original physical namespace-init creation is still unknown")
	}
	ready, err = ownedPIDFDReady(int(namespaceInit.process.Fd()), 0)
	if err != nil || !ready {
		return errors.Join(err, vmmModeFailureV1("the original VMM namespace-init creation has not physically exited"))
	}
	if owner.readonly {
		for _, actor := range owner.actors {
			ready, err := ownedPIDFDReady(int(actor.process.Fd()), 0)
			if err != nil || !ready {
				return errors.Join(err, vmmModeFailureV1("an original read-only control actor has not physically exited"))
			}
		}
		if owner.readonlyGroup == nil {
			return vmmModeFailureV1("the original read-only cgroup proof is missing")
		}
		if err := owner.readonlyGroup.closedExceptCreatorV1(); err != nil {
			return err
		}
		owner.readonlyPhysicalClosed = true
	}
	return nil
}
