package supervisor

import (
	"context"
	"errors"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

func imageSaveFailureV1(message string, cause error) error {
	if cause != nil {
		message += ": " + cause.Error()
	}
	return &proto.Error{Code: "image_save", Message: message, Hint: "Use a running microvm on its original host, then repeat image save."}
}

func (s *Supervisor) captureImageSaveSourceV1(ctx context.Context, selected string) (_ *host.ImageSaveSource, returned error) {
	root := s.owner()
	operation, err := host.BeginSessionOperation(ctx, selected, 5*time.Second)
	if err != nil {
		return nil, err
	}
	retained := false
	defer func() {
		if !retained {
			returned = errors.Join(returned, operation.CloseWithError())
		}
	}()
	if operation.Primary() != root.id {
		return nil, imageSaveFailureV1("the selected session belongs to another sandbox", nil)
	}
	protected, err := operation.LoadContext(ctx)
	if err != nil {
		return nil, err
	}
	if protected.Wall == nil || protected.Wall.Level != "microvm" {
		return nil, host.UserError("image_wall", "image save needs a running microvm sandbox", "Start a microvm sandbox, set up its tools, then save its image.")
	}
	if protected.Life == nil || protected.Life.Phase != host.LifeRunning {
		return nil, host.UserError("sandbox_stopped", "the sandbox is not running", "Resume its microvm before saving its image.")
	}
	root.mu.Lock()
	live := root.m != nil && root.rec.phase == host.LifeRunning && root.launched && root.link != nil && root.ctl != nil && root.runtimeCleaned != nil && root.generation != "" && protected.Run.Generation == root.generation && protected.Wall != nil && protected.Wall.Level == "microvm"
	running := root.rec.phase == host.LifeRunning && root.launched
	original := root.lifetimeContext()
	connection := root.vmStartConnection
	var public *api.SessionSecretState
	if root.m != nil {
		public = api.CloneSessionSecretState(root.m.SecretState)
	}
	decoys := api.CloneDecoyTripwires(root.bootDecoyTripwires)
	root.mu.Unlock()
	if !running {
		return nil, host.UserError("sandbox_stopped", "the original microvm is no longer running", "Resume it before saving its image.")
	}
	if !live || connection == nil {
		return nil, host.UserError("sandbox_changed", "the original running microvm changed", "Use its current complete session address and original connection.")
	}
	source, err := host.CaptureImageSaveSource(ctx, original, operation, connection, public, decoys)
	if err != nil {
		return nil, err
	}
	retained = true // The actual source now owns this same OP through its close.
	return source, nil
}

// stopImageSaveRuntimeV1 is used only after an explicit original guest export
// cancellation failed. The SAME actual runtime owner kills/waits, then its
// concrete native owner must positively project physical exit. A logical Wait
// error, status, channel EOF or copied identifier never releases the source OP.
func (s *Supervisor) stopImageSaveRuntimeV1() error {
	root := s.owner()
	root.loseLink("the original image export did not confirm cleanup")
	root.mu.Lock()
	runtime := root.sb
	root.mu.Unlock()
	if runtime == nil {
		return imageSaveFailureV1("the original image export has no retained runtime owner", nil)
	}
	failure := errors.Join(runtime.Kill(), runtime.Wait())
	if !root.imageSavePhysicalExitV1() {
		return imageSaveFailureV1("retain the image source until its original native runtime exit is proved", failure)
	}
	return failure
}
func (s *Supervisor) imageSavePhysicalExitV1() bool {
	root := s.owner()
	root.mu.Lock()
	runtime := root.sb
	root.mu.Unlock()
	original, ok := runtime.(vmRuntime)
	if !ok || original.Sandbox == nil {
		return false
	}
	physical, ok := original.Sandbox.(interface{ PhysicalExitV1() bool })
	return ok && physical.PhysicalExitV1()
}
