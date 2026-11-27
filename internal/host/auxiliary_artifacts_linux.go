//go:build linux

package host

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"runtime"
	"sync"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/runtimecache"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/vmneed"
	"github.com/iamlakshya/simstim/internal/wall"
	"github.com/iamlakshya/simstim/internal/wall/microvm"
)

type auxiliaryRuntimeFileV1 struct {
	item     supply.Item
	file     *os.File
	identity os.FileInfo
}

// auxiliaryArtifactsV1 captures real checked storage descriptors from the
// SAME protected saved source and actual native host domain. The old Need is
// selection DATA. Neither it nor these facts can create an auxiliary boot;
// the genuine new activation/reservation owner must consume them separately.
type auxiliaryArtifactsV1 struct {
	slot                    *slotColdArtifactsV2
	pending                 []io.Closer
	vacancy                 *auxiliaryVacancyV1
	placement               *placement
	domain                  cachefs.Domain
	need                    vmneed.NeedV1
	spec                    wall.Spec
	runtime                 []*auxiliaryRuntimeFileV1
	image, agents, helper   *os.File
	disk                    *os.File
	diskParent              *os.Root
	diskRef                 cachefs.Reference
	imageLease, agentsLease *image.Lease
	helperIdentity          os.FileInfo
	once                    sync.Once
	closeErr                error
}

func captureAuxiliaryArtifactsV1(ctx context.Context, vacancy *auxiliaryVacancyV1) (*auxiliaryArtifactsV1, error) {
	return captureAuxiliaryArtifactsOwnedV2(ctx, vacancy, nil)
}

func captureAuxiliaryArtifactsOwnedV2(ctx context.Context, vacancy *auxiliaryVacancyV1, slot *slotColdArtifactsV2) (created *auxiliaryArtifactsV1, returned error) {
	if vacancy == nil {
		return nil, auxiliaryUnavailableV1("artifact capture has no original native vacancy owner")
	}
	if err := vacancy.Check(ctx); err != nil {
		return nil, err
	}
	snapshot, err := vacancy.operation.LoadContext(ctx)
	if err != nil {
		return nil, err
	}
	if snapshot.Wall == nil || snapshot.Wall.Level != "microvm" {
		return nil, auxiliaryUnavailableV1("this native auxiliary artifact slice requires the recorded microvm source")
	}
	need, err := vmneed.DecodeV1(snapshot.VMNeedV1, runtime.GOARCH)
	if err != nil {
		return nil, err
	}
	if err := need.ValidateWall(runtime.GOARCH, snapshot.Wall.Level, snapshot.Wall.Driver, snapshot.Limits.MemoryMax); err != nil {
		return nil, err
	}
	if snapshot.RuntimeManifest == nil || !reflect.DeepEqual(*snapshot.RuntimeManifest, need.Runtime) {
		return nil, auxiliaryUnavailableV1("the saved runtime projection differs from the original protected Need")
	}
	owner, err := placementForCall()
	if err != nil {
		return nil, err
	}
	if err := owner.checkIdentity(); err != nil {
		return nil, err
	}
	if slot != nil && (slot.owner == nil || slot.owner.warm == nil || slot.owner.warm.owner == nil || owner != slot.owner.warm.owner.origin) {
		return nil, auxiliaryUnavailableV1("the reserved artifacts changed their actual original operator placement")
	}
	result := &auxiliaryArtifactsV1{vacancy: vacancy, placement: owner, domain: owner.domain(), need: need.Clone()}
	if slot != nil {
		if slot.issuer != slot || slot.artifacts == nil || slot.vacancy != vacancy {
			return nil, auxiliaryUnavailableV1("the slot artifacts lost their actual stopped constructor")
		}
		result = slot.artifacts
		result.placement, result.domain, result.need = owner, owner.domain(), need.Clone()
	}
	complete := false
	defer func() {
		if !complete {
			closeErr := result.Close()
			returned = errors.Join(returned, closeErr)
			if closeErr != nil || slot != nil {
				created = result
			}
		}
	}()
	runtimeStore, err := runtimecache.OpenHostReadOnly(ctx, paths.ArtefactsDir(), result.domain)
	if err != nil {
		if runtimeStore != nil {
			result.pending = append(result.pending, runtimeStore)
		}
		return nil, err
	}
	runtimeClosed := false
	defer func() {
		if !runtimeClosed {
			closeErr := runtimeStore.Close()
			returned = errors.Join(returned, closeErr)
			if closeErr != nil {
				result.pending = append(result.pending, runtimeStore)
			}
		}
	}()
	for _, item := range result.need.Runtime.Items {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		file, err := runtimeStore.OpenItem(ctx, item)
		var retained *auxiliaryRuntimeFileV1
		if file != nil {
			retained = &auxiliaryRuntimeFileV1{item: item, file: file}
			result.runtime = append(result.runtime, retained)
		}
		if err != nil {
			return nil, err
		}
		info, err := file.Stat()
		if err != nil {
			return nil, err
		}
		retained.identity = info
	}
	if err := runtimeStore.Close(); err != nil {
		runtimeClosed = true
		result.pending = append(result.pending, runtimeStore)
		return nil, err
	}
	runtimeClosed = true
	images, err := image.OpenHostExisting(ctx, paths.ImagesDir(), result.domain)
	if err != nil {
		if images != nil {
			result.pending = append(result.pending, images)
		}
		return nil, err
	}
	imagesClosed := false
	defer func() {
		if !imagesClosed {
			closeErr := images.Close()
			returned = errors.Join(returned, closeErr)
			if closeErr != nil {
				result.pending = append(result.pending, images)
			}
		}
	}()
	result.image, result.imageLease, _, err = images.OpenImageExisting(ctx, result.need.Image)
	if err != nil {
		return nil, err
	}
	result.agents, result.agentsLease, err = images.OpenAgentsV1(ctx, result.need.Agents)
	if err != nil {
		return nil, err
	}
	if err := images.Close(); err != nil {
		imagesClosed = true
		result.pending = append(result.pending, images)
		return nil, err
	}
	imagesClosed = true
	result.helper, err = os.Open("/proc/self/exe")
	if err != nil {
		return nil, err
	}
	result.helperIdentity, err = result.helper.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(owner.executableRef, result.helperIdentity) {
		return nil, auxiliaryUnavailableV1("the auxiliary helper differs from the actual original placement executable")
	}
	boot := result.need.BootV1.Clone()
	if slot == nil {
		result.diskParent, err = cachefs.OpenPrivateChild(vacancy.operation.native.root, "vm", false)
		if err != nil {
			return result, err
		}
		result.disk, err = cachefs.OpenFile(result.diskParent, microvm.SessionDiskName, os.O_RDONLY, 0600)
		if err != nil {
			return nil, err
		}
		result.diskRef, err = cachefs.CaptureFile(result.diskParent, microvm.SessionDiskName, result.disk, 0600)
		if err != nil {
			return nil, err
		}
		diskInfo, err := result.disk.Stat()
		if err != nil || diskInfo.Size() != result.need.DiskMax {
			return nil, errors.Join(auxiliaryUnavailableV1("the original native saved disk is absent or differs from its recorded size"), err)
		}
	} else {
		result.disk, err = duplicateSlotSnapshotFileV1(slot.roles[9])
		if err != nil {
			return result, err
		}
		if err := slot.checkDiskV2(ctx, result, false); err != nil {
			return result, err
		}
	}
	result.spec = wall.Spec{Driver: result.need.Driver, ID: snapshot.ID, SessionDir: paths.SessionDir(snapshot.ID), VM: &boot, MemoryMax: snapshot.Limits.MemoryMax, CPUs: result.need.CPUs, DiskMax: result.need.DiskMax}
	if err := result.CheckBeforeActivation(ctx); err != nil {
		return nil, err
	}
	complete = true
	return result, nil
}
func (artifacts *auxiliaryArtifactsV1) CheckBeforeActivation(ctx context.Context) error {
	if artifacts == nil || ctx == nil || artifacts.vacancy == nil || artifacts.placement == nil || artifacts.image == nil || artifacts.agents == nil || artifacts.helper == nil {
		return auxiliaryUnavailableV1("the auxiliary artifact source has no retained native descriptors")
	}
	if err := artifacts.vacancy.Check(ctx); err != nil {
		return err
	}
	return artifacts.checkRetainedDescriptorsV1(ctx)
}
func (artifacts *auxiliaryArtifactsV1) checkRetainedDescriptorsV1(ctx context.Context) error {
	if artifacts == nil || ctx == nil || artifacts.vacancy == nil || artifacts.placement == nil || artifacts.image == nil || artifacts.agents == nil || artifacts.helper == nil {
		return auxiliaryUnavailableV1("the original artifact descriptors ended")
	}
	if artifacts.disk == nil {
		return auxiliaryUnavailableV1("the original saved disk descriptor ended")
	}
	if artifacts.slot != nil {
		return errors.Join(artifacts.slot.checkDiskV2(ctx, artifacts, false), artifacts.checkRetainedBootDescriptorsV1(ctx))
	}
	if err := artifacts.diskRef.CheckFile(artifacts.diskParent, microvm.SessionDiskName, artifacts.disk, 0600); err != nil {
		return err
	}
	return artifacts.checkRetainedBootDescriptorsV1(ctx)
}

// A real handed native runtime may write its SAME session disk. This path
// requires the actual new creator and exact native unit before allowing those
// data writes; it never relaxes the immutable pre-activation disk check.
func (artifacts *auxiliaryArtifactsV1) checkAfterNativeTakeoverV1(ctx context.Context, cold *ColdResumeV1, launch *startupLaunchV1) error {
	if artifacts == nil || cold == nil || launch == nil || cold.artifacts != artifacts || cold.startup != launch || !cold.bound || !cold.handed || cold.closed || cold.operation == nil || cold.operation.startup != launch || cold.operation.nativeColdResume != cold || launch.operation != cold.operation || artifacts.vacancy != cold.vacancy || artifacts.disk == nil {
		return auxiliaryUnavailableV1("mutable disk checks need the same actual native takeover")
	}
	current, err := cold.operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	if err := errors.Join(launch.check(ctx), launch.checkNativeMemoryV1(ctx, current)); err != nil {
		return err
	}
	if artifacts.slot != nil {
		return errors.Join(artifacts.slot.checkHandedV2(ctx, artifacts, cold, launch), artifacts.checkRetainedBootDescriptorsV1(ctx))
	}
	if err := artifacts.diskRef.CheckCreatedFile(artifacts.diskParent, microvm.SessionDiskName, artifacts.disk, 0600); err != nil {
		return err
	}
	info, err := artifacts.disk.Stat()
	if err != nil || info.Size() != artifacts.need.DiskMax {
		return errors.Join(auxiliaryUnavailableV1("the handed native disk changed its original fixed size"), err)
	}
	return artifacts.checkRetainedBootDescriptorsV1(ctx)
}

func (artifacts *auxiliaryArtifactsV1) checkRetainedBootDescriptorsV1(ctx context.Context) error {
	if artifacts == nil || ctx == nil || artifacts.vacancy == nil || artifacts.placement == nil || artifacts.image == nil || artifacts.agents == nil || artifacts.helper == nil {
		return auxiliaryUnavailableV1("the original retained boot roles ended")
	}
	if err := artifacts.placement.checkIdentity(); err != nil {
		return err
	}
	if artifacts.placement.domain() != artifacts.domain {
		return auxiliaryUnavailableV1("the original auxiliary host storage domain changed")
	}
	snapshot, err := artifacts.vacancy.operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	encoded, err := vmneed.EncodeV1(artifacts.need, runtime.GOARCH)
	if err != nil {
		return err
	}
	parsed, err := vmneed.DecodeV1(snapshot.VMNeedV1, runtime.GOARCH)
	if err != nil {
		return err
	}
	current, err := vmneed.EncodeV1(*parsed, runtime.GOARCH)
	if err != nil {
		return err
	}
	if !bytes.Equal(encoded, current) {
		if err := checkColdRestartSelectionV1(artifacts.vacancy.operation, artifacts.vacancy.need, artifacts.vacancy.runtime, snapshot); err != nil {
			return err
		}
	}
	for _, role := range artifacts.runtime {
		info, err := role.file.Stat()
		if err != nil || !os.SameFile(role.identity, info) {
			return errors.Join(auxiliaryUnavailableV1("an original runtime descriptor changed"), err)
		}
		if err := supply.CheckOpenContext(ctx, role.item, role.file); err != nil {
			return err
		}
	}
	root, err := image.Inspect(artifacts.image)
	if err != nil {
		return err
	}
	if root.RootHash != artifacts.need.Image.RootHash || root.DataBlocks != artifacts.need.Image.DataBlocks {
		return auxiliaryUnavailableV1("the original checked auxiliary image root changed")
	}
	if err := supply.CheckContent(artifacts.need.Agents, artifacts.agents); err != nil {
		return err
	}
	helper, err := artifacts.helper.Stat()
	if err != nil || !os.SameFile(artifacts.helperIdentity, helper) {
		return errors.Join(auxiliaryUnavailableV1("the original auxiliary helper descriptor changed"), err)
	}
	return ctx.Err()
}
func (artifacts *auxiliaryArtifactsV1) Close() error {
	if artifacts == nil {
		return nil
	}
	if artifacts.slot != nil {
		return artifacts.slot.closeV2(artifacts)
	}
	return artifacts.closeFilesV2()
}

func (artifacts *auxiliaryArtifactsV1) closeFilesV2() error {
	artifacts.once.Do(func() {
		for _, resource := range artifacts.pending {
			artifacts.closeErr = errors.Join(artifacts.closeErr, resource.Close())
		}
		for _, role := range artifacts.runtime {
			artifacts.closeErr = errors.Join(artifacts.closeErr, role.file.Close())
		}
		for _, file := range []*os.File{artifacts.image, artifacts.agents, artifacts.helper, artifacts.disk} {
			if file != nil {
				artifacts.closeErr = errors.Join(artifacts.closeErr, file.Close())
			}
		}
		if artifacts.diskParent != nil {
			artifacts.closeErr = errors.Join(artifacts.closeErr, artifacts.diskParent.Close())
		}
		if artifacts.imageLease != nil {
			artifacts.closeErr = errors.Join(artifacts.closeErr, artifacts.imageLease.Close())
		}
		if artifacts.agentsLease != nil {
			artifacts.closeErr = errors.Join(artifacts.closeErr, artifacts.agentsLease.Close())
		}
	})
	return artifacts.closeErr
}
