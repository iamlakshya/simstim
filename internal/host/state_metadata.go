package host

import (
	"context"
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
)

func publishStateMetadata(ctx context.Context, role stateMetadataRole, data []byte) (returned error) {
	if err := CheckCaller(); err != nil {
		return err
	}
	if err := validateStateMetadata(role, data); err != nil {
		return err
	}
	publisher, err := beginStateMetadataPublisher(ctx)
	if err != nil {
		return stateMetadataFailure(err)
	}
	defer func() { returned = stateMetadataFailure(errors.Join(returned, publisher.close())) }()
	return publishStateMetadataOwned(publisher.ctx, publisher.operation, role, data)
}

func publishStateMetadataOwned(ctx context.Context, operation *placementOperation, role stateMetadataRole, data []byte) error {
	if err := CheckCaller(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateStateMetadata(role, data); err != nil {
		return err
	}
	return withPlacementRoot(ctx, false, func(control *placementControl, guard *os.Root) error {
		if err := checkStateMetadataPublisher(ctx, control, operation); err != nil {
			return err
		}
		state, err := cachefs.OpenPrivateRoot(paths.StateDir(), false)
		if err != nil {
			return err
		}
		defer state.Close()
		if err := checkStateMetadataRoot(control, state); err != nil {
			return err
		}
		if err := replayStateMetadata(ctx, guard, control, state, role); err != nil {
			return err
		}
		_, before, err := readStateMetadata(ctx, state, role)
		if err != nil {
			return err
		}
		key := "state/" + string(role)
		expected, recorded := control.Refs[key]
		if recorded && (len(expected.extra) != 0 || expected.UID != uint64(os.Getuid()) || os.FileMode(expected.Mode) != 0o600 || expected.Inode == 0) {
			return placementError("the state metadata receipt is unknown or belongs to another role")
		}
		if before != nil && (!recorded || !expected.matches(before)) {
			return stateMetadataPreserved(role, "its current native file has no exact producer receipt")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		after, err := writeOwnedMetadataIntent(state, string(role), data, before, func(info os.FileInfo, temporary string) error {
			return prepareStateMetadataWrite(ctx, guard, control, state, role, before, info, temporary)
		})
		if err != nil {
			return err
		}
		if err := checkStateMetadataRoot(control, state); err != nil {
			return err
		}
		if control.Refs == nil {
			control.Refs = map[string]purgeRef{}
		}
		control.Refs[key] = refOf(after)
		delete(control.StateWrites, string(role))
		return nil
	}, true)
}

// prepareStateMetadataWrite runs under the caller's final placement gate. The
// canonical metadata writer supplies the already-synced prepared inode info.
func prepareStateMetadataWrite(ctx context.Context, guard *os.Root, control *placementControl, state *os.Root, role stateMetadataRole, before, after os.FileInfo, temporary string) error {
	if err := CheckCaller(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validStateMetadataRole(role) || len(control.extra) != 0 || after == nil {
		return placementError("the state preparation has an unknown role or control schema")
	}
	if err := checkStateMetadataRoot(control, state); err != nil {
		return err
	}
	if err := checkStateMetadataGuard(control, guard); err != nil {
		return err
	}
	intent := metadataWriteIntent{After: refOf(after), Temporary: temporary}
	if before != nil {
		ref := refOf(before)
		intent.Before = &ref
	}
	if !metadataWriteIntentKnown(intent) {
		return placementError("the prepared state metadata has no exact native receipt")
	}
	if control.StateWrites == nil {
		control.StateWrites = map[string]metadataWriteIntent{}
	}
	control.StateWrites[string(role)] = intent
	return writePlacementControl(guard, *control)
}
