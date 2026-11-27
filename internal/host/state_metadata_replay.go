package host

import (
	"context"
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// replayStateMetadata cancels an unpublished part or reconciles an already
// published inode. It never publishes a prepared part, including during purge.
func replayStateMetadata(ctx context.Context, guard *os.Root, control *placementControl, state *os.Root, role stateMetadataRole) error {
	if err := CheckCaller(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validStateMetadataRole(role) {
		return placementError("the state replay role is unknown")
	}
	intent, exists := control.StateWrites[string(role)]
	if !exists {
		return nil
	}
	if len(control.extra) != 0 || !metadataWriteIntentKnown(intent) {
		return placementError("the state write intent contains unknown authority")
	}
	if err := checkStateMetadataRoot(control, state); err != nil {
		return err
	}
	if err := checkStateMetadataGuard(control, guard); err != nil {
		return err
	}
	_, current, err := readStateMetadata(ctx, state, role)
	if err != nil {
		return err
	}
	if current != nil && intent.After.matches(current) {
		for _, leaf := range []string{intent.Temporary, intent.Quarantine} {
			if leaf == "" {
				continue
			}
			if _, err := state.Lstat(leaf); !errors.Is(err, os.ErrNotExist) {
				return placementError("preserved an unexpected state temporary beside the published inode")
			}
		}
		if control.Refs == nil {
			control.Refs = map[string]purgeRef{}
		}
		control.Refs["state/"+string(role)] = refOf(current)
		return finishStateReplay(ctx, guard, control, role)
	}
	beforeMatches := intent.Before == nil && current == nil || intent.Before != nil && current != nil && intent.Before.matches(current)
	if !beforeMatches {
		return placementError("preserved state metadata that matches neither recorded generation")
	}
	if intent.Before != nil {
		if !control.Refs["state/"+string(role)].matches(current) {
			return placementError("the old state metadata lacks its exact retained receipt")
		}
	}
	if err := cancelStatePrepared(ctx, guard, control, state, role, intent); err != nil {
		return err
	}
	return finishStateReplay(ctx, guard, control, role)
}

func finishStateReplay(ctx context.Context, guard *os.Root, control *placementControl, role stateMetadataRole) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkStateMetadataGuard(control, guard); err != nil {
		return err
	}
	delete(control.StateWrites, string(role))
	return writePlacementControl(guard, *control)
}

func cancelStatePrepared(ctx context.Context, guard *os.Root, control *placementControl, state *os.Root, role stateMetadataRole, intent metadataWriteIntent) error {
	if intent.Quarantine != "" {
		_, err := state.Lstat(intent.Quarantine)
		if err == nil {
			if _, err := state.Lstat(intent.Temporary); !errors.Is(err, os.ErrNotExist) {
				return placementError("preserved both state temporary and cleanup capture")
			}
			return removeReplayedStateCapture(ctx, control, state, role, intent)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	file, err := cachefs.OpenFile(state, intent.Temporary, os.O_RDONLY, 0o600)
	if errors.Is(err, os.ErrNotExist) {
		// A failed creator or completed capture cleanup may have already
		// removed this exact unpublished part. The old receipt stays intact.
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !intent.After.matches(info) {
		return placementError("preserved a changed prepared state temporary")
	}
	if err := validateArchiveDescriptor(ctx, file, func(data []byte) error { return validateStateMetadata(role, data) }); err != nil {
		return err
	}
	capture := intent.Quarantine
	if capture == "" {
		nonce, err := footprintNonce()
		if err != nil {
			return err
		}
		capture = ".metadata-cleanup-" + nonce
	}
	return removeCapturedFile(state, intent.Temporary, intent.After, capture, func(leaf string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := checkStateMetadataRoot(control, state); err != nil {
			return err
		}
		intent.Quarantine = leaf
		control.StateWrites[string(role)] = intent
		return writePlacementControl(guard, *control)
	}, func(file *os.File) error {
		return validateArchiveDescriptor(ctx, file, func(data []byte) error { return validateStateMetadata(role, data) })
	})
}

func removeReplayedStateCapture(ctx context.Context, control *placementControl, state *os.Root, role stateMetadataRole, intent metadataWriteIntent) error {
	file, err := cachefs.OpenFile(state, intent.Quarantine, os.O_RDONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !intent.After.matches(info) {
		return placementError("preserved a state cleanup capture with another native identity")
	}
	if err := validateArchiveDescriptor(ctx, file, func(data []byte) error { return validateStateMetadata(role, data) }); err != nil {
		return err
	}
	if err := checkStateMetadataRoot(control, state); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := cachefs.ValidateFile(state, intent.Quarantine, file, 0o600)
	if err != nil || !intent.After.matches(current) {
		return placementError("the captured state temporary changed before unlink")
	}
	if err := state.Remove(intent.Quarantine); err != nil {
		return err
	}
	return syncCapturedDirectory(state)
}
