//go:build linux

package host

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"runtime"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/checkpoint"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/vmneed"
	"github.com/iamlakshya/simstim/internal/wall"
)

func (b *bridge) prepareRewindV1(service *bridgeRewindServiceV1, request proto.Request) (_ *api.RewindPrepareResultV1, returned error) {
	var args api.RewindPrepareArgsV1
	if request.ID == 0 || request.Op != api.OpRewindPrepareV1 || json.Unmarshal(request.Args, &args) != nil || args.Validate() != nil {
		return nil, rewindFailureV1("the original rewind preparation metadata is invalid")
	}
	digest, err := api.RewindPreparationDigestV1(args.Selection, args.Start, args.Floor)
	if err != nil {
		return nil, err
	}
	if prior := service.plan.Load(); prior != nil {
		if prior.selection != args.Selection || prior.startDigest != digest || prior.floor != args.Floor || prior.consumed {
			return nil, rewindFailureV1("this connection already owns another original rewind")
		}
		if err := prior.checkPreparationV1(prior.work); err != nil {
			return nil, err
		}
		result := prior.preparedResultV1()
		return &result, nil
	}
	metadata, err := cloneRewindStartV1(args.Start)
	if err != nil {
		return nil, err
	}
	p := &bridgeRewindPreparationV1{bridge: b, selection: args.Selection, metadata: metadata, floor: args.Floor, startDigest: digest, prepareRequest: request.ID, lifetime: b.ctx}
	base, cancelExecution := context.WithCancelCause(context.Background())
	p.execution = context.WithValue(base, bridgeRewindPreparationContextKeyV1{}, p)
	p.cancelExecution = cancelExecution
	p.work, p.cancel = context.WithCancelCause(p.execution)
	p.stopClientWatch = context.AfterFunc(b.ctx, func() { p.cancel(context.Canceled) })
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		cancelExecution(err)
		return nil, err
	}
	p.token = hex.EncodeToString(nonce[:])
	service.plan.Store(p)
	complete := false
	defer func() {
		if !complete {
			cleanup := p.closeOwnedV1()
			returned = errors.Join(returned, cleanup)
			if cleanup == nil && service.plan.Load() == p {
				service.plan.Store(nil)
			}
		}
	}()
	p.operation, err = BeginSessionOperation(p.execution, args.Selection.ID, 5*time.Second)
	if err != nil {
		return nil, err
	}
	if err := p.work.Err(); err != nil {
		return nil, err
	}
	p.active.Store(true)
	p.operation.gate.Lock()
	if p.operation.rewindPreparation != nil || !p.operation.active.Load() {
		p.operation.gate.Unlock()
		return nil, rewindFailureV1("the original operation already owns a rewind")
	}
	p.operation.rewindPreparation = p
	p.operation.gate.Unlock()
	if _, err := p.operation.native.root.Lstat(rewindJournalLeafV1); !errors.Is(err, os.ErrNotExist) {
		return nil, errors.Join(UserError("rewind_pending", "this sandbox has a pending rewind", "Keep its files and finish the original native transaction before preparing another."), err)
	}
	var cold *ColdResumeV1
	p.warm, cold, err = InspectResumeForOperationV1(p.work, p.operation)
	if err != nil {
		return nil, err
	}
	if cold != nil {
		return nil, errors.Join(UserError("sandbox_stopped", "rewind needs its original running sandbox", "Resume the sandbox through its native owner, then prepare rewind again."), cold.Close())
	}
	if p.warm == nil {
		return nil, rewindFailureV1("the original running rewind source is absent")
	}
	p.members, err = captureRewindMembersV1(p.work, p.operation, args.Selection.ID)
	if err != nil {
		return nil, err
	}
	p.source, err = rewindSnapshotFromMembersV1(p.members, args.Selection.ID)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(p.source, args.Start.Snapshot) {
		return nil, snapshotChanged()
	}
	if err := p.checkFloorAndImageV1(p.work, args.Start); err != nil {
		return nil, err
	}
	// This is a prospective actual-request receipt. The original live cap
	// remains reserved; no vacancy or cold accounting exists before Stop.
	if err := admitRestartMemoryRequestV1(p.work, p.operation, args.Start); err != nil {
		return nil, err
	}
	preview, err := p.previewStartV1(p.work, args.Start, false)
	if err != nil {
		return nil, err
	}
	p.keep, err = checkpointKeepCountV1(preview.Policy)
	if err != nil {
		return nil, err
	}
	slot, local, err := p.warm.captureSlotSnapshotReservedV2(p.work, p.operation)
	if slot != nil {
		// Keep actual partial custody before checking the late error. A nil
		// concrete local result must not become a nonnil typed interface.
		p.slotSnapshot = slot
	}
	if err != nil {
		return nil, err
	}
	if local {
		if err := p.probeLocalCheckpointSpaceV1(p.work); err != nil {
			return nil, err
		}
		p.snapshot, err = checkpoint.SelectReadonlyV1(p.work, p.operation.native.root, p.selection.Checkpoint, func(ctx context.Context) error { return p.warm.Check(ctx, p.operation) })
	} else {
		if p.slotSnapshot == nil {
			return nil, rewindFailureV1("the slot Snapshot factory returned no actual owner")
		}
		if _, ok := p.slotSnapshot.(rewindSlotColdV1); !ok {
			return nil, rewindFailureV1("the original slot Snapshot factory has no complete stopped restart adapter")
		}
		p.snapshot, err = p.slotSnapshot.SelectRunningSnapshotV1(p.work, p.selection.Checkpoint)
	}
	if err != nil {
		return nil, err
	}
	if err := p.checkSelectedSnapshotV1(p.work); err != nil {
		return nil, err
	}
	p.phase.Store(rewindPreparedV1)
	if err := p.checkPreparationV1(p.work); err != nil {
		return nil, err
	}
	complete = true
	result := p.preparedResultV1()
	return &result, nil
}

func checkpointKeepCountV1(policy *api.Policy) (int, error) {
	keep := 10
	if policy != nil && policy.Sandbox.Checkpoints != nil {
		keep = *policy.Sandbox.Checkpoints
	}
	if keep < 0 || keep > 10000 {
		return 0, UserError("bad_value", "sandbox.checkpoints must be between 0 and 10000", "Choose a supported number of checkpoints to keep.")
	}
	if keep == 0 {
		return 0, UserError("rewind_off", "checkpoints are off: sandbox.checkpoints is 0", "Enable disk checkpoints in the current policy before preparing rewind.")
	}
	return keep, nil
}

func (p *bridgeRewindPreparationV1) checkFloorAndImageV1(ctx context.Context, start api.ResumeArgs) error {
	root, err := p.operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	if root.Wall == nil || root.Wall.Level != "microvm" {
		return UserError("rewind_off", "rewind needs a microvm session disk", "Use a microvm whose session filesystem supports file cloning.")
	}
	for _, value := range []string{p.floor, root.Wall.Floor} {
		if value != "" {
			floor, err := wall.ParseLevel(value)
			if err != nil || floor > wall.MicroVM {
				return errors.Join(UserError("wall_floor", "the current pinned floor does not allow this recorded microvm", "Keep the sandbox and check the current host floor before restarting it."), err)
			}
		}
	}
	if err := validateRestartWall(root); err != nil {
		return err
	}
	if len(start.ExpectedImageV1) == 0 {
		return UserError("image_policy_changed", "rewind needs the original image checked under current policy", "Prepare rewind with the current client and its retained original image.")
	}
	return checkResumeExpectedImageV1(ctx, p.operation, start.ExpectedImageV1)
}

func (p *bridgeRewindPreparationV1) previewStartV1(ctx context.Context, start api.ResumeArgs, values bool) (*Manifest, error) {
	root, err := p.operation.LoadContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := p.checkFloorAndImageV1(ctx, start); err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(start.Snapshot, p.source) {
		return nil, snapshotChanged()
	}
	if err := ApplyResumeLayers(root, start.Layers); err != nil {
		return nil, err
	}
	if err := ApplyRepositoryResume(root, start); err != nil {
		return nil, err
	}
	if err := applyAdmittedRestartMemoryV1(p.operation, root); err != nil {
		return nil, err
	}
	if root.Wall == nil || root.Wall.Level != "microvm" || root.Policy == nil {
		return nil, rewindFailureV1("the original rewind restart changed its protected wall or policy")
	}
	if err := validateResumeInputs(root, start.Inputs); err != nil {
		return nil, err
	}
	if err := prepareResumeEnvironment(root, start.Environment); err != nil {
		return nil, err
	}
	if values {
		children := make([]*Manifest, 0, len(p.members)-1)
		for _, member := range p.members[1:] {
			current, err := member.read(ctx, p.operation)
			if err != nil || current == nil {
				return nil, errors.Join(snapshotChanged(), err)
			}
			children = append(children, current)
		}
		if _, err := checkedResumeMembers(root.ID, children, start.Members); err != nil {
			return nil, err
		}
		primaryValues, err := checkedResumeValues(root.Secrets, start.Secrets)
		if err != nil {
			return nil, err
		}
		if _, err := prepareResumeSecretDelivery(root, start.SecretDelivery, primaryValues); err != nil {
			return nil, err
		}
		if err := checkNewNamedSSHSelection(*root.Policy, start.NamedSSHKeys); err != nil {
			return nil, err
		}
		for _, member := range start.Members {
			for _, child := range children {
				if child.ID == member.ID {
					values, err := checkedResumeValues(child.Secrets, member.Secrets)
					if err != nil {
						return nil, err
					}
					if _, err := prepareResumeSecretDelivery(child, member.SecretDelivery, values); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	return root, nil
}

func (p *bridgeRewindPreparationV1) probeLocalCheckpointSpaceV1(ctx context.Context) (returned error) {
	if err := p.warm.Check(ctx, p.operation); err != nil {
		return err
	}
	root, err := p.operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	need, err := vmneed.DecodeV1(root.VMNeedV1, runtime.GOARCH)
	if err != nil {
		return err
	}
	vm, err := cachefs.OpenPrivateChild(p.operation.native.root, "vm", false)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, vm.Close()) }()
	checkpoints, err := cachefs.OpenPrivateChild(p.operation.native.root, "checkpoints", false)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, checkpoints.Close()) }()
	space, err := files.SpaceAt(checkpoints)
	if err != nil {
		return err
	}
	if space.Available < uint64(need.DiskMax) {
		return UserError("checkpoint_low_disk", "checkpoints paused: low disk", "Free at least sandbox.disk_max before restoring the disk.")
	}
	return checkpoint.ProbeRootsV1(ctx, vm, checkpoints, func(ctx context.Context) error { return p.warm.Check(ctx, p.operation) })
}

func (p *bridgeRewindPreparationV1) checkSelectedSnapshotV1(ctx context.Context) error {
	if p == nil || p.snapshot == nil || p.operation == nil {
		return rewindFailureV1("the original selected snapshot is absent")
	}
	if err := p.snapshot.CheckV1(ctx); err != nil {
		return err
	}
	root, err := p.operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	need, err := vmneed.DecodeV1(root.VMNeedV1, runtime.GOARCH)
	if err != nil {
		return err
	}
	if need.DiskMax <= 0 || p.snapshot.DiskBytesV1() != need.DiskMax {
		return rewindFailureV1("the selected checkpoint differs from the original admitted disk size")
	}
	if p.snapshot.Entry().Time.Format(checkpoint.TimeLayout) != p.selection.Checkpoint {
		return rewindFailureV1("the retained snapshot changed its original timestamp")
	}
	return checkRewindConversationIDsV1(p.members, p.snapshot.Entry().Metadata)
}

func (p *bridgeRewindPreparationV1) checkPreparationV1(ctx context.Context) error {
	if p == nil || ctx == nil || p.operation == nil || !p.active.Load() || !p.operation.Valid(p.operation.Primary()) || p.bridge == nil {
		return rewindFailureV1("the original rewind preparation ended")
	}
	if p.phase.Load() == rewindCompletedV1 {
		return nil
	}
	if p.consumed {
		return p.checkAccountingOriginV1(p.execution, p.operation, p.cold)
	}
	if err := errors.Join(ctx.Err(), p.warm.Check(ctx, p.operation)); err != nil {
		return err
	}
	for _, member := range p.members {
		current, err := member.read(ctx, p.operation)
		if err != nil || current == nil {
			return errors.Join(snapshotChanged(), err)
		}
		if current.Agent.Kind != member.manifest.Agent.Kind {
			return rewindFailureV1("an original checkpoint member changed agent")
		}
	}
	current, err := SandboxSnapshotLocked(p.selection.ID, p.operation.Primary())
	if err != nil || !reflect.DeepEqual(current, p.source) {
		return errors.Join(snapshotChanged(), err)
	}
	return p.checkSelectedSnapshotV1(ctx)
}

func (p *bridgeRewindPreparationV1) commitRewindV1(request proto.Request, args api.RewindCommitArgsV1) (_ *api.RewindResult, returned error) {
	if request.ID == 0 || request.Op != api.OpRewindCommitV1 || p == nil || args.Selection != p.selection || !args.Prepared.Valid() || args.Prepared.Token != p.token {
		return nil, rewindFailureV1("the commit selects another original rewind")
	}
	digest, err := api.RewindPreparationDigestV1(args.Selection, args.Start, p.floor)
	if err != nil || digest != p.startDigest {
		return nil, errors.Join(rewindFailureV1("the final rewind changed its original start metadata"), err)
	}
	encoded, err := json.Marshal(args.Start)
	if err != nil {
		return nil, err
	}
	defer clear(encoded)
	if p.consumed {
		if !bytes.Equal(encoded, p.commitEncoded) {
			return nil, rewindFailureV1("the rewind retry changed its original private values")
		}
		return p.performRewindV1()
	}
	if err := p.checkPreparationV1(p.work); err != nil {
		return nil, err
	}
	if _, err := p.previewStartV1(p.work, args.Start, true); err != nil {
		return nil, err
	}
	fresh, err := cloneRewindStartV1(args.Start)
	if err != nil {
		return nil, err
	}
	p.fresh = fresh
	p.commitEncoded = bytes.Clone(encoded)
	p.commitRequest = request.ID
	p.consumed = true
	return p.performRewindV1()
}
