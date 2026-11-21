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

// User drain is independent of purge's retained control.Drain. It is only a
// setting: no identifier or saved reference here issues native start or stop.
type userDrainV1 struct {
	Version int       `json:"v"`
	HostID  string    `json:"host_id"`
	State   purgeRef  `json:"state_generation"`
	Place   purgeRef  `json:"place_generation"`
	Reason  string    `json:"reason"`
	At      time.Time `json:"at"`
}

// The ahead journal retains the exact original inode through clear and retry.
// A matching filename or a later drain record cannot replace this receipt.
type drainRemovalV1 struct {
	Ref     purgeRef `json:"ref"`
	Capture string   `json:"capture"`
}

func (removal *drainRemovalV1) UnmarshalJSON(data []byte) error {
	type plain drainRemovalV1
	var value plain
	fields, err := api.JSONObjectFields(data, api.ExactJSONFieldNames(value)...)
	if err != nil || len(fields) != 2 || !api.ExactJSONFields(data, value) || json.Unmarshal(data, &value) != nil {
		return placementError("the drain removal has unknown or ambiguous fields")
	}
	*removal = drainRemovalV1(value)
	return validateDrainRemovalV1(removal)
}

func validDrainReasonV1(reason string) bool {
	return reason != "" && reason != "off" && len(reason) <= 512 && strings.TrimSpace(reason) == reason && api.CleanText(reason, 512) == reason
}

func validateDrainDataV1(data []byte) error {
	var value userDrainV1
	fields, err := api.JSONObjectFields(data, api.ExactJSONFieldNames(value)...)
	if err != nil || len(fields) != 6 || !api.ExactJSONFields(data, value) || json.Unmarshal(data, &value) != nil || value.Version != 1 || !ValidHostID(value.HostID) ||
		!value.State.validDirectory() || !validDrainPlaceV1(value.Place) || !validDrainReasonV1(value.Reason) || value.At.IsZero() {
		return stateMetadataPreserved(stateDrain, "its version, reason or native generation is unknown")
	}
	return nil
}

func validDrainPlaceV1(ref purgeRef) bool {
	return len(ref.extra) == 0 && ref.Inode != 0 && ref.UID == uint64(os.Getuid()) && os.FileMode(ref.Mode) == 0o600
}

func validateDrainRemovalV1(removal *drainRemovalV1) error {
	if removal != nil && (!validDrainPlaceV1(removal.Ref) || removal.Ref.Size <= 0 || removal.Ref.ModTime.IsZero() || !strings.HasPrefix(removal.Capture, ".drain-clear-") || !ValidHostID(strings.TrimPrefix(removal.Capture, ".drain-clear-"))) {
		return placementError("the drain removal has no exact native capture receipt")
	}
	return nil
}

// SetDrainContext writes or clears the setting under the original helper's
// placement and control locks. It never touches a running session or unit.
func SetDrainContext(ctx context.Context, args api.DrainArgs) (result api.DrainState, returned error) {
	if ctx == nil {
		return result, UserError("drain", "the drain change has no request lifetime", "Change drain through the current host connection.")
	}
	if args.On && !validDrainReasonV1(args.Reason) || !args.On && args.Reason != "" {
		return result, UserError("bad_drain", "the drain reason must be one plain line of 1 to 512 bytes", "Use drain=off to clear it, or give a short reason for the drain.")
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
		return result, placementError("the drain publisher changed its original helper owner")
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
		if err := replayDrainRemovalV1(publisher.ctx, guard, control, state); err != nil {
			return err
		}
		if err := replayStateMetadata(publisher.ctx, guard, control, state, stateDrain); err != nil {
			return err
		}
		_, before, err := readOwnedUserDrainV1(publisher.ctx, control, state)
		if err != nil {
			return err
		}
		if !args.On {
			if before == nil {
				return nil
			}
			nonce, err := footprintNonce()
			if err != nil {
				return err
			}
			control.DrainRemoval = &drainRemovalV1{Ref: refOf(before), Capture: ".drain-clear-" + nonce}
			if err := writePlacementControl(guard, *control); err != nil {
				return err
			}
			return replayDrainRemovalV1(publisher.ctx, guard, control, state)
		}
		data, err := json.Marshal(userDrainV1{Version: 1, HostID: control.HostID, State: control.State, Place: control.Place, Reason: args.Reason, At: time.Now().UTC()})
		if err != nil {
			return err
		}
		after, err := writeOwnedMetadataIntent(state, string(stateDrain), data, before, func(info os.FileInfo, temporary string) error {
			return prepareStateMetadataWrite(publisher.ctx, guard, control, state, stateDrain, before, info, temporary)
		})
		if err != nil {
			return err
		}
		if control.Refs == nil {
			control.Refs = map[string]purgeRef{}
		}
		control.Refs["state/"+string(stateDrain)] = refOf(after)
		delete(control.StateWrites, string(stateDrain))
		result = api.DrainState{On: true, Reason: args.Reason}
		return nil
	}, true)
	return result, err
}

// readUserDrainLockedV1 is read-only. Callers already hold the native control
// and placement locks. Pending writes/removals stay unknown and fail closed;
// observations never repair them or acquire a new original operation.
func readUserDrainLockedV1(ctx context.Context, control *placementControl, state *os.Root) (api.DrainState, error) {
	value, _, err := readOwnedUserDrainV1(ctx, control, state)
	return value, err
}

func readOwnedUserDrainV1(ctx context.Context, control *placementControl, state *os.Root) (api.DrainState, os.FileInfo, error) {
	var result api.DrainState
	if err := checkStateMetadataRoot(control, state); err != nil {
		return result, nil, err
	}
	if _, pending := control.StateWrites[string(stateDrain)]; pending || control.DrainRemoval != nil {
		return result, nil, stateMetadataPreserved(stateDrain, "the original change is still awaiting reconciliation")
	}
	data, info, err := readStateMetadata(ctx, state, stateDrain)
	if err != nil {
		return result, nil, err
	}
	if info == nil {
		if _, recorded := control.Refs["state/"+string(stateDrain)]; recorded {
			return result, nil, stateMetadataPreserved(stateDrain, "its recorded native file is missing")
		}
		return result, nil, nil
	}
	expected, recorded := control.Refs["state/"+string(stateDrain)]
	var value userDrainV1
	if !recorded || !expected.matches(info) || len(expected.extra) != 0 || json.Unmarshal(data, &value) != nil || value.HostID != control.HostID ||
		!samePurgeDirectoryRef(value.State, control.State) || !nativeReferenceEqual(apiPurgeRef(value.Place), apiPurgeRef(control.Place)) {
		return result, nil, stateMetadataPreserved(stateDrain, "its original host, root or native producer receipt changed")
	}
	return api.DrainState{On: true, Reason: value.Reason}, info, nil
}

// checkUserDrainLockedV1 belongs only in actual new/manual restart admission.
// Automatic restore may skip this setting; purge's separate gate still holds.
func checkUserDrainLockedV1(ctx context.Context, control *placementControl) error {
	state, err := cachefs.OpenPrivateRoot(paths.StateDir(), false)
	if err != nil {
		return err
	}
	defer state.Close()
	mark, err := readUserDrainLockedV1(ctx, control, state)
	if err != nil {
		return err
	}
	if mark.On {
		return UserError("draining", "this host is draining: "+mark.Reason, "Clear the drain setting with host set drain=off, or choose another host.")
	}
	return nil
}

func replayDrainRemovalV1(ctx context.Context, guard *os.Root, control *placementControl, state *os.Root) error {
	removal := control.DrainRemoval
	if removal == nil {
		return nil
	}
	if err := errors.Join(ctx.Err(), validateDrainRemovalV1(removal), checkStateMetadataRoot(control, state), checkStateMetadataGuard(control, guard)); err != nil {
		return err
	}
	expected, recorded := control.Refs["state/"+string(stateDrain)]
	if !recorded || !nativeReferenceEqual(apiPurgeRef(expected), apiPurgeRef(removal.Ref)) {
		return placementError("the drain clear lost its original metadata receipt")
	}
	check := func(file *os.File) error {
		return validateArchiveDescriptor(ctx, file, validateDrainDataV1)
	}
	_, captureErr := state.Lstat(removal.Capture)
	_, currentErr := state.Lstat(string(stateDrain))
	switch {
	case captureErr == nil && errors.Is(currentErr, os.ErrNotExist):
		file, err := cachefs.OpenFile(state, removal.Capture, os.O_RDONLY, 0o600)
		if err != nil {
			return err
		}
		defer file.Close()
		info, err := cachefs.ValidateFile(state, removal.Capture, file, 0o600)
		if err != nil || !removal.Ref.matches(info) {
			return stateMetadataPreserved(stateDrain, "its captured clear file changed identity")
		}
		if err := check(file); err != nil {
			return err
		}
		current, err := cachefs.ValidateFile(state, removal.Capture, file, 0o600)
		if err != nil || !removal.Ref.matches(current) {
			return stateMetadataPreserved(stateDrain, "its captured file changed before removal")
		}
		if err := state.Remove(removal.Capture); err != nil {
			return err
		}
	case errors.Is(captureErr, os.ErrNotExist) && currentErr == nil:
		if err := removeCapturedFile(state, string(stateDrain), removal.Ref, removal.Capture, func(string) error { return ctx.Err() }, check); err != nil {
			return err
		}
	case errors.Is(captureErr, os.ErrNotExist) && errors.Is(currentErr, os.ErrNotExist):
		// The original unlink completed before its acknowledgement was saved.
	default:
		return stateMetadataPreserved(stateDrain, "its clear found an unexpected native file or capture")
	}
	if err := syncCapturedDirectory(state); err != nil {
		return err
	}
	delete(control.Refs, "state/"+string(stateDrain))
	control.DrainRemoval = nil
	return writePlacementControl(guard, *control)
}
