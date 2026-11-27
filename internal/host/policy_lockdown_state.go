package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
)

// Lockdown is a protected host policy setting. Its native receipts bind only
// this metadata publisher; they issue no runtime, start, account or stop.
type hostLockdownRecordV1 struct {
	Version int       `json:"v"`
	HostID  string    `json:"host_id"`
	State   purgeRef  `json:"state_generation"`
	Place   purgeRef  `json:"place_generation"`
	At      time.Time `json:"at"`
}

// The ahead journal retains the exact original inode through clear and retry.
// A matching filename or a later lockdown record cannot replace this receipt.
type lockdownRemovalV1 struct {
	Ref     purgeRef `json:"ref"`
	Capture string   `json:"capture"`
}

func (removal *lockdownRemovalV1) UnmarshalJSON(data []byte) error {
	type plain lockdownRemovalV1
	var value plain
	fields, err := api.JSONObjectFields(data, api.ExactJSONFieldNames(value)...)
	if err != nil || len(fields) != 2 || !api.ExactJSONFields(data, value) || json.Unmarshal(data, &value) != nil {
		return placementError("the lockdown removal has unknown or ambiguous fields")
	}
	*removal = lockdownRemovalV1(value)
	return validateLockdownRemovalV1(removal)
}

func validateLockdownDataV1(data []byte) error {
	var value hostLockdownRecordV1
	fields, err := api.JSONObjectFields(data, api.ExactJSONFieldNames(value)...)
	if err != nil || len(fields) != 5 || !api.ExactJSONFields(data, value) || json.Unmarshal(data, &value) != nil || value.Version != 1 || !ValidHostID(value.HostID) ||
		!value.State.validDirectory() || !validLockdownPlaceV1(value.Place) || value.At.IsZero() {
		return stateMetadataPreserved(stateLockdown, "its version or native generation is unknown")
	}
	return nil
}

func validLockdownPlaceV1(ref purgeRef) bool {
	return len(ref.extra) == 0 && ref.Inode != 0 && ref.UID == uint64(os.Getuid()) && os.FileMode(ref.Mode) == 0o600
}

func validateLockdownRemovalV1(removal *lockdownRemovalV1) error {
	if removal != nil && (!validLockdownPlaceV1(removal.Ref) || removal.Ref.Size <= 0 || removal.Ref.ModTime.IsZero() || !strings.HasPrefix(removal.Capture, ".lockdown-clear-") || !ValidHostID(strings.TrimPrefix(removal.Capture, ".lockdown-clear-"))) {
		return placementError("the lockdown removal has no exact native capture receipt")
	}
	return nil
}

// SetPersistentLockdownContextV1 writes or clears the setting under the original helper's
// placement and control locks. It never touches a running session or unit.
func SetPersistentLockdownContextV1(ctx context.Context, on bool) (result bool, returned error) {
	if ctx == nil {
		return result, UserError("lockdown", "the lockdown change has no request lifetime", "Change lockdown through the current saved host connection.")
	}

	// The facts entry is existing-only and excludes installation payloads.
	owner, err := factsPlacementOwnerV1(ctx)
	if err != nil {
		return result, err
	}
	publisher, err := beginStateMetadataPublisher(ctx)
	if err != nil {
		return result, stateMetadataFailure(err)
	}
	defer func() { returned = stateMetadataFailure(errors.Join(returned, publisher.close())) }()
	if publisher.operation.owner != owner {
		return result, placementError("the lockdown publisher changed its original helper owner")
	}
	err = withPlacementRoot(publisher.ctx, false, func(control *placementControl, guard *os.Root) error {
		if err := checkStateMetadataPublisher(publisher.ctx, control, publisher.operation); err != nil {
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
		if err := replayLockdownRemovalV1(publisher.ctx, guard, control, state); err != nil {
			return err
		}
		if err := replayStateMetadata(publisher.ctx, guard, control, state, stateLockdown); err != nil {
			return err
		}
		_, before, err := readOwnedLockdownV1(publisher.ctx, control, state)
		if err != nil {
			return err
		}
		if !on {
			if before == nil {
				return nil
			}
			nonce, err := footprintNonce()
			if err != nil {
				return err
			}
			control.LockdownRemoval = &lockdownRemovalV1{Ref: refOf(before), Capture: ".lockdown-clear-" + nonce}
			if err := writePlacementControl(guard, *control); err != nil {
				return err
			}
			return replayLockdownRemovalV1(publisher.ctx, guard, control, state)
		}
		data, err := json.Marshal(hostLockdownRecordV1{Version: 1, HostID: control.HostID, State: control.State, Place: control.Place, At: time.Now().UTC()})
		if err != nil {
			return err
		}
		after, err := writeOwnedMetadataIntent(state, string(stateLockdown), data, before, func(info os.FileInfo, temporary string) error {
			return prepareStateMetadataWrite(publisher.ctx, guard, control, state, stateLockdown, before, info, temporary)
		})
		if err != nil {
			return err
		}
		if control.Refs == nil {
			control.Refs = map[string]purgeRef{}
		}
		control.Refs["state/"+string(stateLockdown)] = refOf(after)
		delete(control.StateWrites, string(stateLockdown))
		result = true
		return nil
	}, true)
	return result, err
}

// readPersistentLockdownLockedV1 is read-only. Callers already hold the native control
// and placement locks. Pending writes/removals stay unknown and fail closed;
// observations never repair them or acquire a new original operation.
func readPersistentLockdownLockedV1(ctx context.Context, control *placementControl, state *os.Root) (bool, error) {
	value, _, err := readOwnedLockdownV1(ctx, control, state)
	return value, err
}

func readOwnedLockdownV1(ctx context.Context, control *placementControl, state *os.Root) (bool, os.FileInfo, error) {
	var result bool
	if err := checkStateMetadataRoot(control, state); err != nil {
		return result, nil, err
	}
	if _, pending := control.StateWrites[string(stateLockdown)]; pending || control.LockdownRemoval != nil {
		return result, nil, stateMetadataPreserved(stateLockdown, "the original change is still awaiting reconciliation")
	}
	data, info, err := readStateMetadata(ctx, state, stateLockdown)
	if err != nil {
		return result, nil, err
	}
	if info == nil {
		if _, recorded := control.Refs["state/"+string(stateLockdown)]; recorded {
			return result, nil, stateMetadataPreserved(stateLockdown, "its recorded native file is missing")
		}
		return result, nil, nil
	}
	expected, recorded := control.Refs["state/"+string(stateLockdown)]
	var value hostLockdownRecordV1
	if !recorded || !expected.matches(info) || len(expected.extra) != 0 || json.Unmarshal(data, &value) != nil || value.HostID != control.HostID ||
		!samePurgeDirectoryRef(value.State, control.State) || !nativeReferenceEqual(apiPurgeRef(value.Place), apiPurgeRef(control.Place)) {
		return result, nil, stateMetadataPreserved(stateLockdown, "its original host, root or native producer receipt changed")
	}
	return true, info, nil
}

// applyPersistentLockdownLockedV1 runs only inside the actual original
// new/restart placement writer. The global setting supplies a deny layer,
// never a native Need, account mode, secret selection or execution receipt.
func applyPersistentLockdownLockedV1(ctx context.Context, control *placementControl, manifest *Manifest) error {
	if manifest == nil || manifest.Wall == nil || IsNoneSupervisor(manifest) || manifest.SandboxOf != "" {
		return nil
	}
	state, err := cachefs.OpenPrivateRoot(paths.StateDir(), false)
	if err != nil {
		return err
	}
	defer state.Close()
	on, err := readPersistentLockdownLockedV1(ctx, control, state)
	if err != nil {
		return err
	}
	policy, layers, err := ApplyPolicyLockdownLayersV1(manifest.Layers, on)
	if err != nil {
		return err
	}
	manifest.Policy, manifest.Layers = &policy, layers
	return nil
}

// ReadPersistentLockdownContextV1 reads only the original helper's protected
// host setting. A public result cannot construct a publisher or native start.
func ReadPersistentLockdownContextV1(ctx context.Context) (on bool, returned error) {
	if err := CheckCaller(); err != nil {
		return false, err
	}
	returned = withPlacement(ctx, false, func(control *placementControl) error {
		state, err := cachefs.OpenPrivateRoot(paths.StateDir(), false)
		if err != nil {
			return err
		}
		defer state.Close()
		on, err = readPersistentLockdownLockedV1(ctx, control, state)
		return err
	}, false)
	return on, returned
}

func replayLockdownRemovalV1(ctx context.Context, guard *os.Root, control *placementControl, state *os.Root) error {
	removal := control.LockdownRemoval
	if removal == nil {
		return nil
	}
	if err := errors.Join(ctx.Err(), validateLockdownRemovalV1(removal), checkStateMetadataRoot(control, state), checkStateMetadataGuard(control, guard)); err != nil {
		return err
	}
	expected, recorded := control.Refs["state/"+string(stateLockdown)]
	if !recorded || !nativeReferenceEqual(apiPurgeRef(expected), apiPurgeRef(removal.Ref)) {
		return placementError("the lockdown clear lost its original metadata receipt")
	}
	check := func(file *os.File) error {
		return validateArchiveDescriptor(ctx, file, validateLockdownDataV1)
	}
	_, captureErr := state.Lstat(removal.Capture)
	_, currentErr := state.Lstat(string(stateLockdown))
	switch {
	case captureErr == nil && errors.Is(currentErr, os.ErrNotExist):
		file, err := cachefs.OpenFile(state, removal.Capture, os.O_RDONLY, 0o600)
		if err != nil {
			return err
		}
		defer file.Close()
		info, err := cachefs.ValidateFile(state, removal.Capture, file, 0o600)
		if err != nil || !removal.Ref.matches(info) {
			return stateMetadataPreserved(stateLockdown, "its captured clear file changed identity")
		}
		if err := check(file); err != nil {
			return err
		}
		current, err := cachefs.ValidateFile(state, removal.Capture, file, 0o600)
		if err != nil || !removal.Ref.matches(current) {
			return stateMetadataPreserved(stateLockdown, "its captured file changed before removal")
		}
		if err := state.Remove(removal.Capture); err != nil {
			return err
		}
	case errors.Is(captureErr, os.ErrNotExist) && currentErr == nil:
		if err := removeCapturedFile(state, string(stateLockdown), removal.Ref, removal.Capture, func(string) error { return ctx.Err() }, check); err != nil {
			return err
		}
	case errors.Is(captureErr, os.ErrNotExist) && errors.Is(currentErr, os.ErrNotExist):
		// The original unlink completed before its acknowledgement was saved.
	default:
		return stateMetadataPreserved(stateLockdown, "its clear found an unexpected native file or capture")
	}
	if err := syncCapturedDirectory(state); err != nil {
		return err
	}
	delete(control.Refs, "state/"+string(stateLockdown))
	control.LockdownRemoval = nil
	return writePlacementControl(guard, *control)
}
