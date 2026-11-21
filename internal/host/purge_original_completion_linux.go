//go:build linux

package host

import (
	"context"
	"errors"
	"os"
)

// This receipt is produced while the SAME original creation descriptor is
// still open, after real successful Cmd.Wait and native pidfd exit. It holds
// exact descriptor objects across later read-only Close acknowledgement errors.
// A saved report, phase, PID or absent file cannot construct it.
type purgeOriginalCompletionV3 struct {
	owner           *purgeOriginalInvocationV3
	executionFiles  []*os.File
	executionRoots  []*os.Root
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
	completion := &purgeOriginalCompletionV3{owner: owner, executionFiles: []*os.File{execution.creation, execution.directory}}
	if execution.loan != nil {
		completion.executionFiles = append(completion.executionFiles, execution.loan.OriginalCode, execution.loan.CopiedCode, execution.loan.ParentPidfd, execution.loan.Cancel)
	}
	for _, peer := range []*nativeStartupPeerV1{execution.peer, execution.line.nativePeer} {
		if peer == nil {
			continue
		}
		completion.executionRoots = append(completion.executionRoots, peer.proc)
		if peer.identity != nil {
			completion.executionFiles = append(completion.executionFiles, peer.identity.executable, peer.identity.user, peer.identity.cgroup)
		}
	}
	return completion, nil
}

func (completion *purgeOriginalCompletionV3) Close(ctx context.Context) error {
	if completion == nil || ctx == nil || completion.owner == nil || completion.owner.completion != completion {
		return placementError("the original completion is not this SAME invocation's native receipt")
	}
	if err := errors.Join(ctx.Err(), CheckCaller()); err != nil {
		return err
	}
	if !completion.closedExecution {
		err := completion.owner.execution.Close()
		observed := observeOriginalPurgeDescriptorCloseV3(completion.executionFiles, completion.executionRoots)
		if observed == nil {
			completion.closedExecution = true
		}
		if err != nil || observed != nil {
			return errors.Join(err, observed)
		}
	} else if err := observeOriginalPurgeDescriptorCloseV3(completion.executionFiles, completion.executionRoots); err != nil {
		return err
	}
	// Factory Close independently proves its own physical files/directory are
	// gone or removes only those SAME actual creations. The native exit receipt
	// grants no cleanup of a session, root, marker or arbitrary temporary path.
	return completion.owner.code.Close()
}
