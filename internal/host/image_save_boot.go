package host

import (
	"bytes"
	"context"
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/image"
)

type imageSaveBootKeyV1 struct{}

// imageSaveBootV1 freezes only exclusion/review DATA from the actual original
// cold-start operation. The native receipt remains separate authority.
type imageSaveBootV1 struct {
	boot             *originalBootV1
	original, actual files.StatePlan
	path             string
}

// CaptureImageSaveBoot must run on the actual received cold-start context,
// before its operation is released or any guest command is allowed to start.
// The private context survives only this same native runtime's lifetime.
func CaptureImageSaveBoot(ctx context.Context) (context.Context, error) {
	original := OriginalVMNeedFromContext(ctx)
	if original == nil {
		return nil, vmNeedError("image-save boot capture has no original native receipt")
	}
	if original.need == nil {
		return ctx, nil
	} // Non-VM walls never gain save.
	if _, present := ctx.Value(imageSaveBootKeyV1{}).(*imageSaveBootV1); present {
		return nil, vmNeedError("the original image-save boot snapshot already exists")
	}
	original.prepareMu.Lock()
	defer original.prepareMu.Unlock()
	original.boot.owner.mu.Lock()
	defer original.boot.owner.mu.Unlock()
	if original.boot.owner.startup != original.boot || !original.boot.owner.Valid(original.boot.loan.Primary) || !bytes.Equal(original.boot.currentNeed, original.rawNeed) {
		return nil, vmNeedError("the original image-save boot owner changed")
	}
	if original.prepareUsed || original.binding != nil || original.boot.ready || !original.boot.operation.Valid(original.boot.loan.Primary) {
		return nil, vmNeedError("image-save boot capture came after native VM preparation")
	}
	manifest, err := original.boot.operation.LoadContext(ctx)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(manifest.VMNeedV1, original.rawNeed) || manifest.ID != original.boot.loan.Primary || manifest.SandboxOf != "" {
		return nil, vmNeedError("the original image-save boot source changed")
	}
	snapshot := &imageSaveBootV1{boot: original.boot, actual: files.StatePlan{Dirs: slices.Clone(original.spec.StateDirs), Files: slices.Clone(original.spec.StateFiles)}}
	if manifest.Files != nil {
		snapshot.original = files.StatePlan{Dirs: slices.Clone(manifest.Files.State.Dirs), Files: slices.Clone(manifest.Files.State.Files)}
	}
	found := false
	for _, value := range manifest.Env {
		name, content, valid := strings.Cut(value, "=")
		if name != "PATH" {
			continue
		}
		if !valid || found {
			return nil, vmNeedError("the original image-save PATH is ambiguous")
		}
		snapshot.path, found = content, true
	}
	if !found {
		return nil, vmNeedError("the original runtime has no frozen PATH for image review")
	}
	if _, err := image.NewSavedRootPlan(snapshot.original, snapshot.actual, snapshot.path, nil, nil); err != nil {
		return nil, err
	}
	return context.WithValue(ctx, imageSaveBootKeyV1{}, snapshot), nil
}
