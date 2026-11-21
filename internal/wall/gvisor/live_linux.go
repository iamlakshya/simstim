//go:build linux

package gvisor

import (
	"context"
	"errors"
	"net"
	"os"

	"github.com/iamlakshya/simstim/internal/wall"
)

// LiveOwnerV1 retains the actual factory-created sandbox and its SAME
// Start-returned connection. It is native evidence only; the host must also
// retain its original OP/controller/runtime source before an agent resume.
type LiveOwnerV1 struct {
	sandbox    *sandbox
	runtime    *Runtime
	connection net.Conn
	command    interface{ CheckNativeLiveV1(context.Context) error }
}

// NativeLiveOwnerV1 cannot adopt another driver, reopened path or status row.
func NativeLiveOwnerV1(runtime wall.Sandbox, connection net.Conn) (*LiveOwnerV1, error) {
	s, ok := runtime.(*sandbox)
	if !ok {
		return nil, nil
	}
	if connection == nil {
		return nil, failure("wall_setup", "the original gvisor connection is absent", nil)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started || !s.hello || s.physical || s.forced || s.released || s.destroyed || s.connection != connection || s.runtime == nil || s.command == nil || s.nativeLive != nil {
		return nil, failure("wall_setup", "the original gvisor runtime cannot issue its live continuation", nil)
	}
	command, ok := s.command.(interface{ CheckNativeLiveV1(context.Context) error })
	if !ok {
		return nil, failure("wall_setup", "the original gvisor command has no retained native checker", nil)
	}
	if err := command.CheckNativeLiveV1(s.owner); err != nil {
		return nil, err
	}
	result := &LiveOwnerV1{sandbox: s, runtime: s.runtime, connection: connection, command: command}
	s.nativeLive = result
	return result, nil
}
func (owner *LiveOwnerV1) Check(ctx context.Context) error {
	if owner == nil || ctx == nil || owner.sandbox == nil || owner.runtime == nil || owner.connection == nil || owner.command == nil {
		return failure("wall_setup", "the original gvisor native owner is absent", nil)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s := owner.sandbox
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nativeLive != owner || !s.started || !s.hello || s.physical || s.forced || s.released || s.destroyed || s.connection != owner.connection || s.runtime != owner.runtime {
		return failure("wall_setup", "the original gvisor native lifetime ended or changed", nil)
	}
	return errors.Join(owner.command.CheckNativeLiveV1(ctx), s.owner.Err(), ctx.Err())
}

// FreezeV1 uses only the actual original command's all-process cgroup. It
// never substitutes a process-group signal or a saved unit/cgroup name.
func (owner *LiveOwnerV1) FreezeV1(ctx context.Context, maxMS int) error {
	if err := owner.Check(ctx); err != nil {
		return err
	}
	freezer, ok := owner.command.(interface {
		FreezeAllProcessesV1(context.Context, int) error
	})
	if !ok {
		return failure("freeze_off", "the actual gvisor command has no native all-process freezer", nil)
	}
	return freezer.FreezeAllProcessesV1(ctx, maxMS)
}

// ThawV1 releases only this original command's owned native freeze lease.
func (owner *LiveOwnerV1) ThawV1() error {
	if owner == nil || owner.command == nil {
		return nil
	}
	freezer, ok := owner.command.(interface{ ThawAllProcessesV1() error })
	if !ok {
		return failure("freeze_off", "the actual gvisor command has no native thaw owner", nil)
	}
	return freezer.ThawAllProcessesV1()
}

// FreezeAvailableV1 reports a currently checked original native capability.
func (owner *LiveOwnerV1) FreezeAvailableV1() bool {
	if owner == nil || owner.sandbox == nil || owner.Check(owner.sandbox.owner) != nil {
		return false
	}
	freezer, ok := owner.command.(interface{ FreezeAvailableV1() bool })
	return ok && freezer.FreezeAvailableV1()
}

// FreezeReasonV1 describes the original command's unavailable capability.
func (owner *LiveOwnerV1) FreezeReasonV1() string {
	if owner == nil || owner.command == nil {
		return "the original gvisor command is absent"
	}
	freezer, ok := owner.command.(interface{ FreezeReasonV1() string })
	if !ok {
		return "the original gvisor command has no native freezer"
	}
	return freezer.FreezeReasonV1()
}

// WithForkRootsV1 borrows the original mapped A's actual mounted work/home
// graph. Its gofer home and canonical state binds use those SAME backing
// objects. The caller freezes all writers inside the callback; no root escapes.
func (owner *LiveOwnerV1) WithForkRootsV1(ctx context.Context, use func(work, home *os.Root) error) error {
	if err := owner.Check(ctx); err != nil {
		return err
	}
	roots, ok := owner.command.(interface {
		WithGvisorForkRootsV2(context.Context, func(*os.Root, *os.Root) error) error
	})
	if !ok {
		return failure("fork_off", "the actual gvisor command has no original mapped-root fork source", nil)
	}
	if err := roots.WithGvisorForkRootsV2(ctx, use); err != nil {
		return err
	}
	return owner.Check(ctx)
}
