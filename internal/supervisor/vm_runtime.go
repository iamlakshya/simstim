package supervisor

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/wall"
)

type vmRuntime struct {
	wall.Sandbox
	disk *diskResourcesV1
}

func (r vmRuntime) ownedDiskResourcesV1() *diskResourcesV1 { return r.disk }

func (r vmRuntime) Start(ctx context.Context) (net.Conn, error) {
	conn, err := r.disk.start(ctx, r.Sandbox.Start)
	return conn, vmRuntimeError("sandbox_boot", err)
}
func (r vmRuntime) Wait() error {
	exit := r.Sandbox.Wait()
	if exit.Err != nil {
		return vmRuntimeError("sandbox_error", exit.Err)
	}
	if exit.Code != 0 || exit.Signal != "" {
		return vmRuntimeError("sandbox_error", fmt.Errorf("the owned VMM exited with code %d signal %s", exit.Code, exit.Signal))
	}
	return nil
}
func (r vmRuntime) CleanExit() bool {
	checked, ok := r.Sandbox.(interface{ CleanExit() bool })
	return ok && checked.CleanExit()
}
func (r vmRuntime) PowerButton(ctx context.Context) error {
	control, ok := r.Sandbox.(interface{ PowerButton(context.Context) error })
	if !ok {
		return fmt.Errorf("the selected runtime has no owned VMM power control")
	}
	return control.PowerButton(ctx)
}
func vmRuntimeError(code string, err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var wire *proto.Error
	if errors.As(err, &wire) {
		return err
	}
	var mismatch *supply.MismatchError
	if errors.As(err, &mismatch) {
		code = "digest_mismatch"
	}
	return &proto.Error{Code: code, Message: err.Error(), Hint: "Read the session's supervisor.log and check the host runtime before resuming."}
}

// PhysicalExitV1 preserves the concrete native owner through this adapter.
func (r vmRuntime) PhysicalExitV1() bool {
	owner, ok := r.Sandbox.(interface{ PhysicalExitV1() bool })
	return ok && owner.PhysicalExitV1()
}
