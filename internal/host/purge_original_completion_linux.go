//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"os/exec"
)

// This receipt is produced while the SAME original creation descriptor is
// still open, after real successful Cmd.Wait and native pidfd exit. It holds
// exact descriptor objects across later read-only Close acknowledgement errors.
// A saved report, phase, PID or absent file cannot construct it.
type purgeOriginalCompletionV3 struct {
	owner           *purgeOriginalInvocationV3
	execution       *purgeAttemptExecV1
	code            *purgeAttemptCodeV2
	command         *exec.Cmd
	state           *os.ProcessState
	closedExecution bool
}

func newPurgeOriginalCompletionV3(owner *purgeOriginalInvocationV3) (*purgeOriginalCompletionV3, error) {
	if owner == nil || owner.execution == nil || owner.code == nil || owner.execution.code != owner.code {
		return nil, placementError("the original completion has no SAME code factory and actual creation")
	}
	execution := owner.execution
	if !execution.admissionSent || execution.start == nil || execution.start.creator != execution || execution.line == nil || execution.peer == nil || execution.peer.identity == nil || execution.loan == nil || execution.loan.creator != owner.code {
		return nil, placementError("the original completion has no same actual admitted fixed native creator")
	}
	if err := execution.start.checkRetainedSpecV3(context.Background()); err != nil {
		return nil, err
	}
	owner.code.mu.Lock()
	bound := owner.code.execution == execution
	owner.code.mu.Unlock()
	if !bound {
		return nil, placementError("the original completion lost its actual factory execution issuer")
	}
	if err := execution.checkJoinedCreationV2(); err != nil {
		return nil, err
	}
	if execution.command.ProcessState == nil || !execution.command.ProcessState.Success() || execution.waitErr != nil {
		return nil, placementError("the actual original fixed purge process did not exit successfully; its private cleanup remains uncertain")
	}
	completion := &purgeOriginalCompletionV3{owner: owner, execution: execution, code: owner.code, command: execution.command, state: execution.command.ProcessState}
	return completion, nil
}

func (completion *purgeOriginalCompletionV3) Close(ctx context.Context) error {
	if completion == nil || ctx == nil || completion.owner == nil || completion.owner.completion != completion || completion.owner.execution != completion.execution || completion.owner.code != completion.code || completion.execution == nil || completion.execution.command != completion.command || completion.command == nil || completion.command.ProcessState != completion.state || completion.state == nil || !completion.state.Success() || completion.execution.waitErr != nil {
		return placementError("the original completion is not this SAME invocation's native receipt")
	}
	if err := errors.Join(ctx.Err(), CheckCaller()); err != nil {
		return err
	}
	// The actual execution owner recorded its first closes, joined real I/O
	// and preserved the original Cmd.Wait. No descriptor observer can replace
	// that owner's immutable native retirement facts after closure.
	diagnostic := completion.execution.Close()
	if native := completion.execution.checkNativeRetiredV3(ctx); native != nil {
		return errors.Join(diagnostic, native)
	}
	completion.closedExecution = true
	diagnostic = errors.Join(diagnostic, completion.execution.CloseDiagnosticsV3(), completion.code.Close())
	if native := completion.code.checkNativeRetiredV3(ctx); native != nil {
		return errors.Join(diagnostic, native)
	}
	return errors.Join(diagnostic, completion.code.CloseDiagnosticsV3())
}
