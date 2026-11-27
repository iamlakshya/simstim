//go:build linux

package guest

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"

	"golang.org/x/sys/unix"
)

type vmShutdownOps struct {
	ready    func() error
	sync     func()
	powerOff func() error
}

func (v *vmRuntime) shutdown() error {
	return v.shutdownWith(vmShutdownOps{sync: unix.Sync, ready: v.emitStateReady, powerOff: func() error {
		err := powerOffGuest()
		if err == nil {
			return vmError("guest power off unexpectedly returned to PID1")
		}
		return err
	}})
}

func (v *vmRuntime) shutdownWith(ops vmShutdownOps) error {
	v.cancel()
	v.mu.Lock()
	preparation := v.prepareDone
	v.mu.Unlock()
	if preparation != nil {
		<-preparation
	}
	if v.network != nil {
		v.network.Close()
	}
	if v.memory != nil {
		if err := v.memory.scope.close(); err != nil {
			v.setError(err)
			return err
		}
	}
	ops.sync()
	if err := v.files.pivotBack(); err != nil {
		v.setError(err)
		return err
	}
	if err := v.files.close(); err != nil {
		v.setError(err)
		return err
	}
	v.mu.Lock()
	prior := v.endErr
	v.mu.Unlock()
	if prior != nil {
		return prior
	}
	if ops.ready != nil {
		if err := ops.ready(); err != nil {
			v.setError(err)
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if v.init.ctlSt != nil {
		if err := v.init.ctlSt.Flush(ctx); err != nil {
			v.setError(err)
			return err
		}
	}

	if err := ops.powerOff(); err != nil {
		failure := fmt.Errorf("power off the guest after cleanup: %w", err)
		v.setError(failure)
		return failure
	}
	return nil
}

// emitStateReady runs only AFTER all copy, process, sync and filesystem cleanup
// succeeded. The host still requires its same owned clean natural exit.
func (v *vmRuntime) emitStateReady() error {
	if v.state == nil || !v.state.ready {
		return nil
	}
	data, _ := json.Marshal(proto.CtlEvent{Event: proto.EventStateReady, Session: v.plan.Args.Session, CaptureNonce: v.plan.Args.CaptureNonce})
	v.init.mu.Lock()
	writer, stream := v.init.ctl, v.init.ctlSt
	v.init.mu.Unlock()
	if writer == nil || stream == nil {
		return vmError("the pending VM state has no control channel")
	}
	_ = stream.SetWriteDeadline(time.Now().Add(ctlCloseWait))
	if err := writer.Write(proto.TEvent, data); err != nil {
		return vmError("the pending VM state event did not reach its bounded control transport")
	}
	return nil
}
