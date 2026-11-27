package supervisor

import (
	"context"
	"errors"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

// checkCurrentV1 consumes the same original actual controller/engine scope.
// It locks itself; callers must not hold root.mu. Completion/phase fields
// only narrow this retained native owner and never create one.
func (scope *nativeStopScopeV1) checkCurrentV1(ctx context.Context) error {
	if err := scope.checkNativeRuntimeV1(ctx); err != nil {
		return err
	}
	scope.mu.Lock()
	operation := scope.operation
	scope.mu.Unlock()
	if operation == nil || !operation.Valid(scope.state.Primary) {
		return execRefusal("the warm request has no original borrowed incoming operation")
	}
	return nil
}
func (scope *nativeStopScopeV1) checkNativeRuntimeV1(ctx context.Context) error {
	if scope == nil || ctx == nil || scope.root == nil || scope.owner == nil || scope.purpose != proto.NativeResumeInspectV1 || scope.lifetime == nil {
		return execRefusal("the original warm controller scope is absent")
	}
	if err := errors.Join(ctx.Err(), scope.lifetime.Err()); err != nil {
		return err
	}
	root := scope.root
	root.mu.Lock()
	live := root.bootOwner == scope.owner && root.generation == scope.state.Generation && root.runtimeCleaned == scope.cleaned && !channelClosed(root.runtimeCleaned) && root.sb != nil && root.launched && root.rec.phase == host.LifeRunning && root.link != nil && root.link.Err() == nil
	runtime, connection, process := root.sb, root.vmStartConnection, root.processCopy
	gvisor := root.gvisorLive
	lifetime := root.lifetimeContext()
	sameNative := lifetime == scope.lifetime && host.OriginalVMNeedFromContext(lifetime) == scope.native && connection == scope.connection && process == scope.process && gvisor == scope.gvisor && root.noneLive == scope.none
	root.mu.Unlock()
	if !live || !sameNative || !scope.owner.Valid(root.id) || !scope.owner.StartupCommittedV1() {
		return execRefusal("the original warm controller ended or changed")
	}
	switch runtime.(type) {
	case noneRuntime:
		if scope.none == nil {
			return execRefusal("the unwalled runtime has no original native continuation")
		}
		return scope.none.Check(ctx)
	case vmRuntime:
		receipt := host.OriginalVMNeedFromContext(lifetime)
		if receipt == nil {
			return execRefusal("the warm VM has no original native receipt")
		}
		return errors.Join(receipt.CheckRunningNativeV1(lifetime, connection), ctx.Err())
	case processSandbox:
		if gvisor != nil {
			return gvisor.Check(ctx)
		}
		if process == nil {
			return execRefusal("the warm process backend has no original live native continuation")
		}
		return process.CheckLiveV1(ctx)
	default:
		return execRefusal("the selected warm runtime has no supported original native continuation")
	}
}
