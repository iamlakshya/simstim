//go:build linux

package microvm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/jail"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
)

// Only the actual concrete Cmd + opaque original mode source + SAME held VMM
// can construct this phase. No callback, role flag, record or saved PID does.
// Cmd is the sole reader of the captured sentinel; EOF alone never restores.
type vmmExecPhaseV1 struct {
	mu                       sync.Mutex
	work                     sync.Mutex
	command                  *jail.Cmd
	source                   *supply.ExecModeLeaseV1
	program                  *os.File // borrowed from the original VerifiedRuntime owner
	control                  *net.UnixConn
	reader, writer           *os.File
	ctx                      context.Context
	session, driver          string
	childAccepted, completed bool
}

func newVMMExecPhaseV1(ctx context.Context, command *jail.Cmd, source *supply.ExecModeLeaseV1, program *os.File, control net.Conn, session, driver string) (*vmmExecPhaseV1, error) {
	if command == nil || source == nil || program == nil {
		return nil, fmt.Errorf("direct VMM phase has no original native constructor")
	}
	local, ok := control.(*net.UnixConn)
	if !ok {
		return nil, fmt.Errorf("direct VMM phase lost its original control socket")
	}
	if err := source.CheckProgramV1(ctx, program); err != nil {
		return nil, err
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	phase := &vmmExecPhaseV1{command: command, source: source, program: program, control: local, reader: reader, writer: writer, ctx: ctx, session: session, driver: driver}
	if err := command.RetainVMMExecLeaseV1(source); err != nil {
		reader.Close()
		writer.Close()
		return nil, err
	}
	abort := func(err error) (*vmmExecPhaseV1, error) {
		return nil, errors.Join(err, command.SettleVMMUnstartedV1(source))
	}
	if err := command.RetainVMMFinalBoundaryV1(source, reader, writer); err != nil {
		reader.Close()
		writer.Close()
		return abort(err)
	}
	if err := command.BindVMMExecControlV1(source, local); err != nil {
		reader.Close()
		writer.Close()
		return abort(err)
	}
	return phase, nil
}

func (phase *vmmExecPhaseV1) acceptExecChild(message runnerMessage, control *proto.UnixControlV1) error {
	phase.mu.Lock()
	defer phase.mu.Unlock()
	if phase.completed || phase.childAccepted || message.Session != phase.session || message.Driver != phase.driver || len(control.Files) != 1 {
		return fmt.Errorf("VMM final child is duplicate, late or from another original mode phase")
	}
	if err := control.CheckReceivedV1(phase.control); err != nil {
		return err
	}
	if err := phase.command.AcceptVMMExecChildV1(phase.ctx, phase.source, phase.control, control); err != nil {
		return err
	}
	phase.childAccepted = true
	return nil
}

func (phase *vmmExecPhaseV1) run(ctx context.Context, controller *runnerControl) error {
	phase.work.Lock()
	defer phase.work.Unlock()
	if err := phase.command.WaitVMMProjectionV1(ctx, phase.source); err != nil {
		return err
	}
	// The actual final helper is now adopted and has both native roles. No
	// parent writer is closed at Cmd.Start or an intermediate helper exec.
	if err := phase.writer.Close(); err != nil {
		return err
	}
	phase.writer = nil
	if err := phase.command.ArmVMMExecModeV1(ctx, phase.source); err != nil {
		return err
	}
	if err := controller.continueModeV1(ctx, phase); err != nil {
		return err
	}
	if err := phase.command.WaitVMMFinalBoundaryV1(ctx, phase.source); err != nil {
		return err
	}
	if err := phase.command.FinishVMMExecModeV1(ctx, phase.source); err != nil {
		return err
	}
	phase.mu.Lock()
	phase.completed = true
	phase.mu.Unlock()
	return phase.reader.Close()
}

// settleNoFuture is used only by the original engine's physical cleanup.
// Unknown startup errors retain Cmd/source/parent copies until that actual
// whole-source proof is available; an ordinary EOF never enters this path.
func (phase *vmmExecPhaseV1) settleNoFuture(ctx context.Context) error {
	if phase == nil {
		return nil
	}
	phase.work.Lock()
	defer phase.work.Unlock()
	phase.mu.Lock()
	completed := phase.completed
	phase.mu.Unlock()
	if completed {
		return phase.source.Close()
	}
	if err := phase.command.WaitVMMNoFutureExecV1(ctx, phase.source); err != nil {
		return err
	}
	if phase.writer != nil {
		if err := phase.writer.Close(); err != nil {
			return err
		}
		phase.writer = nil
	}
	if err := phase.command.FinishVMMExecModeV1(ctx, phase.source); err != nil {
		return err
	}
	phase.mu.Lock()
	phase.completed = true
	phase.mu.Unlock()
	return errors.Join(phase.reader.Close(), phase.source.Close())
}

func (c *runnerControl) attachModePhaseV1(phase *vmmExecPhaseV1) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if phase == nil || c.mode != nil || c.gotReady || c.gotExit || c.conn != phase.control {
		return fmt.Errorf("VMM control cannot acquire another actual mode phase")
	}
	reader, ok := c.ancillary.(*nativeLineReader)
	if !ok {
		return fmt.Errorf("VMM mode phase has no original native byte reader")
	}
	reader.modeMu.Lock()
	reader.mode = phase
	reader.modeMu.Unlock()
	c.mode = phase
	return nil
}

func (c *runnerControl) continueModeV1(ctx context.Context, phase *vmmExecPhaseV1) error {
	c.mu.Lock()
	same := c.mode == phase
	c.mu.Unlock()
	if !same {
		return fmt.Errorf("VMM mode continuation lost its original control owner")
	}
	select {
	case c.callGate <- struct{}{}:
		defer func() { <-c.callGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(5 * time.Second)
	}
	if err := c.conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { c.conn.SetWriteDeadline(time.Now()); close(interrupted) })
	defer func() {
		if !stop() {
			<-interrupted
		}
		c.conn.SetWriteDeadline(time.Time{})
	}()
	if err := c.encoder.Encode(runnerRequest{Op: "mode_continue_v1"}); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	return ctx.Err()
}
