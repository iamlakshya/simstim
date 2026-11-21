package host

import (
	"context"
	"errors"
	"os"
	"slices"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/checkpoint"
	"github.com/iamlakshya/simstim/internal/paths"
)

// rewindMemberRootV1 is retained by the same original primary operation.
// A member's saved SandboxOf alone cannot issue its directory role.
type rewindMemberRootV1 struct {
	id       string
	root     *os.Root
	rootRef  cachefs.Reference
	receipt  *memberRootReceiptV1
	manifest *Manifest
	closed   bool
	closeErr error
}

func captureRewindMembersV1(ctx context.Context, operation *SessionOperation, selected string) (result []*rewindMemberRootV1, returned error) {
	if ctx == nil || operation == nil || operation.native == nil || operation.native.borrowed || !operation.Valid(operation.Primary()) {
		return nil, rewindFailureV1("the rewind cohort has no original owning operation")
	}
	root, err := operation.native.root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	primary := &rewindMemberRootV1{id: operation.Primary(), root: root, rootRef: operation.native.rootRef}
	result = append(result, primary)
	owned := append([]*rewindMemberRootV1(nil), result...)
	complete := false
	defer func() {
		if !complete {
			for _, member := range owned {
				returned = errors.Join(returned, member.close())
			}
			result = nil
		}
	}()
	primary.manifest, err = operation.LoadContext(ctx)
	if err != nil {
		return nil, err
	}
	if primary.manifest.Wall == nil || primary.manifest.Wall.Level != "microvm" {
		return nil, UserError("rewind_off", "checkpoints are off: this wall has no microvm session disk", "Use a microvm whose session filesystem supports file cloning.")
	}
	var receipts map[string]memberRootReceiptV1
	err = withPlacement(ctx, false, func(control *placementControl) error {
		receipts = make(map[string]memberRootReceiptV1)
		for id, receipt := range control.MemberRoots {
			if receipt.Primary == operation.Primary() {
				receipts[id] = receipt
			}
		}
		return validateMemberRootReceiptsV1(receipts)
	}, false)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(receipts))
	for id := range receipts {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	if len(ids) > 255 {
		return nil, rewindFailureV1("the rewind cohort exceeds the checkpoint metadata bound")
	}
	found := selected == operation.Primary()
	for _, id := range ids {
		receipt := receipts[id]
		if err := receipt.PrimaryRef.matchesRoot(operation.native.root); err != nil {
			return nil, err
		}
		child, err := cachefs.OpenPrivateRoot(paths.SessionDir(id), false)
		if err != nil {
			return nil, err
		}
		member := &rewindMemberRootV1{id: id, root: child, receipt: &receipt}
		result = append(result, member)
		owned = append(owned, member)
		member.rootRef, err = cachefs.CaptureRoot(child)
		if err != nil {
			return nil, err
		}
		if err := receipt.Directory.matchesRoot(child); err != nil {
			return nil, err
		}
		member.manifest, err = readMemberRootManifestV1(child, id, receipt)
		if err != nil || member.manifest == nil {
			return nil, errors.Join(rewindFailureV1("the original member manifest is absent or changed"), err)
		}
		found = found || id == selected
	}
	if !found {
		return nil, rewindFailureV1("the selected session is not in the original checkpoint cohort")
	}
	// Compare the ordinary complete snapshot codec after every root was pinned.
	listed, err := SandboxMembers(operation.Primary())
	if err != nil {
		return nil, err
	}
	if len(listed) != len(ids) {
		return nil, snapshotChanged()
	}
	for _, member := range listed {
		if _, exists := receipts[member.ID]; !exists {
			return nil, snapshotChanged()
		}
	}
	complete = true
	return result, nil
}

func (member *rewindMemberRootV1) read(ctx context.Context, operation *SessionOperation) (*Manifest, error) {
	if member == nil || member.root == nil || ctx == nil || operation == nil || !operation.Valid(operation.Primary()) {
		return nil, rewindFailureV1("the original rewind member root ended")
	}
	if err := errors.Join(ctx.Err(), member.rootRef.CheckRoot(member.root)); err != nil {
		return nil, err
	}
	if member.receipt == nil {
		if member.id != operation.Primary() {
			return nil, snapshotChanged()
		}
		if err := operation.native.rootRef.CheckRoot(member.root); err != nil {
			return nil, err
		}
		return operation.LoadContext(ctx)
	}
	if err := errors.Join(member.receipt.PrimaryRef.matchesRoot(operation.native.root), member.receipt.Directory.matchesRoot(member.root)); err != nil {
		return nil, err
	}
	return readMemberRootManifestV1(member.root, member.id, *member.receipt)
}

func (member *rewindMemberRootV1) close() error {
	if member == nil || member.root == nil {
		return nil
	}
	if member.closeErr != nil {
		return member.closeErr
	}
	if member.closed {
		return nil
	}
	member.closeErr = member.root.Close()
	member.closed = member.closeErr == nil
	return member.closeErr
}

func rewindSnapshotFromMembersV1(members []*rewindMemberRootV1, selected string) (*api.SandboxSnapshot, error) {
	if len(members) == 0 || members[0].manifest == nil {
		return nil, snapshotChanged()
	}
	children := make([]*Manifest, 0, len(members)-1)
	for _, member := range members[1:] {
		if member.manifest == nil {
			return nil, snapshotChanged()
		}
		children = append(children, member.manifest)
	}
	return snapshotFromProtected(members[0].manifest, children, selected)
}

func checkRewindConversationIDsV1(members []*rewindMemberRootV1, metadata checkpoint.Metadata) error {
	if len(metadata.AgentSessionIDs) != len(members) {
		return rewindFailureV1("the checkpoint and current sandbox have different members")
	}
	for _, member := range members {
		id, exists := metadata.AgentSessionIDs[member.id]
		if !exists || member.manifest == nil {
			return rewindFailureV1("the checkpoint is missing an original member conversation")
		}
		if id != "" {
			agent, known := agents.Find(member.manifest.Agent.Kind)
			if !known || agent.CheckID(id) != nil {
				return rewindFailureV1("a checkpoint conversation ID does not match its original agent")
			}
		}
	}
	return nil
}
