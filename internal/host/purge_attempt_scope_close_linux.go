//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"os/exec"
)

// This source is born only after the real original creation has joined, all
// actual manager jobs have ended, its SAME group is vacant and its unit has
// disappeared. It permits retrying Close observations without reopening them.
type purgeAttemptScopeClosureV3 struct {
	retirement purgeNativeRetirementsV4
	command    *exec.Cmd
	state      *os.ProcessState
	creation   *os.File
	scope      *purgeAttemptScopeV3
	files      []*os.File
	roots      []*os.Root
	started    bool
	managerErr error
}

func newPurgeAttemptScopeClosureV3(scope *purgeAttemptScopeV3) (*purgeAttemptScopeClosureV3, error) {
	if scope == nil || scope.owner == nil || scope.owner.scope != scope || scope.closure != nil || scope.manager == nil || !scope.dispatched {
		return nil, placementError("the scope closure has no actual original dispatched owner")
	}
	if err := errors.Join(scope.checkTerminalForReapV3(), scope.owner.checkJoinedCreationV2(), nativePurgeCreationExitedV3(scope.owner), scope.events.checkOnlyOriginalJobsV3(scope.job, scope.stopJob), scope.requireAbsentV3(scope.manager.ctx)); err != nil {
		return nil, err
	}
	if scope.stopCall != nil {
		if err := scope.settleOriginalStopV3(); err != nil {
			return nil, err
		}
	}
	if scope.group != nil {
		if err := scope.checkOriginalGroupV3(); err != nil {
			return nil, err
		}
		empty, err := originalStopCgroupEmptyV1(scope.group)
		if err != nil || !empty {
			return nil, errors.Join(placementError("the original group has no positive native vacancy"), err)
		}
	}
	closure := &purgeAttemptScopeClosureV3{scope: scope, command: scope.owner.command, state: scope.owner.command.ProcessState, creation: scope.owner.creation, files: []*os.File{scope.group, scope.adoption}, roots: []*os.Root{scope.manager.root}}
	if scope.manager.peer != nil {
		closure.roots = append(closure.roots, scope.manager.peer.proc)
	}
	return closure, nil
}

// CloseV3 releases only this original scope closure and reobserves its same
// physical descriptors after a lost close acknowledgement.
func (closure *purgeAttemptScopeClosureV3) CloseV3() error {
	if closure == nil || closure.scope == nil || closure.scope.closure != closure || closure.scope.owner == nil || closure.scope.owner.scope != closure.scope {
		return placementError("the scope close retry lost its original native closure source")
	}
	scope := closure.scope
	if err := scope.owner.checkJoinedCreationV2(); err != nil {
		return err
	}
	if !closure.started {
		closure.started = true
		closed := closure.retirement.closeFilesV4(closure.files)
		scope.manager.bus.RemoveSignal(scope.signals)
		diagnostic, unknown := retireNativeManagerV4(context.Background(), scope.manager)
		closure.managerErr = errors.Join(closed, diagnostic, unknown)
		if scope.watchDone != nil {
			<-scope.watchDone
		}
	}
	native := closure.checkNativeRetiredV3(context.Background())
	if native == nil {
		scope.closed = true
	}
	return errors.Join(closure.managerErr, native)
}

// The genuine constructor already proved original unit vacancy, native
// pidfd exit and real Cmd.Wait before any close. Retain that actual receipt;
// a later check never reopens this now-retired creation descriptor.
func (closure *purgeAttemptScopeClosureV3) checkNativeRetiredV3(ctx context.Context) error {
	if closure == nil || ctx == nil || closure.scope == nil || closure.scope.closure != closure || !closure.started || closure.scope.owner == nil || closure.scope.owner.scope != closure.scope || closure.command == nil || closure.scope.owner.command != closure.command || closure.state == nil || closure.command.ProcessState != closure.state || closure.creation != closure.scope.owner.creation || closure.command.Process == nil || closure.state.Pid() != closure.command.Process.Pid {
		return placementError("the original scope retirement lost its actual native creation/wait receipt")
	}
	select {
	case <-closure.scope.owner.waitDone:
	default:
		return placementError("the original scope creator has no actual joined Wait")
	}
	return errors.Join(closure.retirement.checkFilesV4(closure.files), closure.scope.manager.checkNativeRetiredV3(context.Background()))
}

func (scope *purgeAttemptScopeV3) checkNativeRetiredV3(ctx context.Context) error {
	if scope == nil || ctx == nil {
		return placementError("the original scope retirement is absent")
	}
	if scope.closure != nil {
		return scope.closure.checkNativeRetiredV3(ctx)
	}
	if scope.unstarted != nil {
		return scope.unstarted.checkNativeRetiredV3(ctx)
	}
	return placementError("the original scope has no genuine physical cleanup producer")
}

func (scope *purgeAttemptScopeV3) CloseDiagnosticsV3() error {
	if scope == nil {
		return nil
	}
	var diagnostics error
	if scope.closure != nil {
		diagnostics = errors.Join(diagnostics, scope.closure.managerErr, scope.closure.retirement.diagnosticsV4())
	}
	if scope.unstarted != nil {
		diagnostics = errors.Join(diagnostics, scope.unstarted.managerErr, scope.unstarted.retirement.diagnosticsV4())
	}
	if scope.manager != nil {
		diagnostics = errors.Join(diagnostics, scope.manager.CloseDiagnosticsV3())
	}
	return diagnostics
}

// This separate resource producer handles an original child which really
// joined before THIS factory ever dispatched its only manager creation call.
// It grants no unit/session stop or deletion authority.
func newUnusedPurgeScopeClosureV4(scope *purgeAttemptScopeV3) (*purgeAttemptScopeClosureV3, error) {
	if scope == nil || scope.owner == nil || scope.owner.scope != scope || scope.manager == nil || scope.closure != nil || scope.dispatched || scope.call != nil || scope.reply != nil || scope.job != nil || scope.stopCall != nil || scope.stopJob != nil || scope.group != nil {
		return nil, placementError("the unused scope has another actual creation or manager dispatch")
	}
	if err := errors.Join(scope.checkTerminalForReapV3(), scope.owner.checkJoinedCreationV2(), nativePurgeCreationExitedV3(scope.owner), scope.requireAbsentV3(scope.manager.ctx)); err != nil {
		return nil, err
	}
	return &purgeAttemptScopeClosureV3{scope: scope, command: scope.owner.command, state: scope.owner.command.ProcessState, creation: scope.owner.creation, files: []*os.File{scope.adoption}}, nil
}
