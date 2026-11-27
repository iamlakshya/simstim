//go:build linux

package host

import (
	"context"
	"errors"
	"os"
)

// This actual constructor owns only its manager connection and public latch.
// It has not started a child or dispatched any unit mutation. The same native
// objects remain reachable after a lost physical Close acknowledgement.
type purgeAttemptScopeUnstartedCloseV3 struct {
	retirement purgeNativeRetirementsV4
	scope      *purgeAttemptScopeV3
	manager    *nativeUserManagerV1
	adoption   *os.File
	watchDone  chan struct{}
	started    bool
	managerErr error
}

func (scope *purgeAttemptScopeV3) closeUnstartedV3() error {
	if scope == nil {
		return nil
	}
	if scope.dispatched || scope.call != nil || scope.owner != nil && scope.owner.process != nil {
		return placementError("a started scope cannot use unstarted cleanup")
	}
	if scope.manager == nil {
		return placementError("the unstarted scope lost its original manager constructor")
	}
	if scope.unstarted == nil {
		scope.unstarted = &purgeAttemptScopeUnstartedCloseV3{scope: scope, manager: scope.manager, adoption: scope.adoption, watchDone: scope.watchDone}
	}
	return scope.unstarted.closeV3()
}

func (closure *purgeAttemptScopeUnstartedCloseV3) closeV3() error {
	if closure == nil || closure.scope == nil || closure.scope.unstarted != closure || closure.manager == nil || closure.scope.manager != closure.manager || closure.scope.adoption != closure.adoption || closure.scope.dispatched || closure.scope.call != nil || closure.scope.owner != nil && closure.scope.owner.process != nil {
		return placementError("the unstarted close retry has another original native constructor")
	}
	if !closure.started {
		closure.started = true
		closed := closure.retirement.closeFilesV4([]*os.File{closure.adoption})
		if closure.watchDone != nil {
			closure.manager.bus.RemoveSignal(closure.scope.signals)
		}
		diagnostic, unknown := retireNativeManagerV4(context.Background(), closure.manager)
		closure.managerErr = errors.Join(closed, diagnostic, unknown)
		if closure.watchDone != nil {
			<-closure.watchDone
		}
	}
	native := closure.checkNativeRetiredV3(context.Background())
	if native == nil {
		closure.scope.closed = true
	}
	return errors.Join(closure.managerErr, native)
}

func (closure *purgeAttemptScopeUnstartedCloseV3) checkNativeRetiredV3(ctx context.Context) error {
	if closure == nil || ctx == nil || closure.scope == nil || closure.scope.unstarted != closure || closure.scope.manager != closure.manager || closure.scope.adoption != closure.adoption || !closure.started {
		return placementError("the unstarted scope has no original completed resource cleanup")
	}
	return errors.Join(closure.retirement.checkFilesV4([]*os.File{closure.adoption}), closure.manager.checkNativeRetiredV3(context.Background()))
}
