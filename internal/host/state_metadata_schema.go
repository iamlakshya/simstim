package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/proto"
)

type stateMetadataRole string

const (
	stateWalls       stateMetadataRole = "walls.json"
	stateIdle        stateMetadataRole = "idle.json"
	stateDrain       stateMetadataRole = "drain.json"
	stateLockdown    stateMetadataRole = "lockdown.json"
	stateFootprint   stateMetadataRole = "footprint.json"
	stateRestoreGate stateMetadataRole = "restore.lock"
)

func validStateMetadataRole(role stateMetadataRole) bool {
	return role == stateWalls || role == stateIdle || role == stateDrain || role == stateLockdown || role == stateFootprint || role == stateRestoreGate
}

type stateMetadataError struct {
	wire  *proto.Error
	cause error
}

func (err *stateMetadataError) Error() string { return err.wire.Message }
func (err *stateMetadataError) Unwrap() error { return err.cause }
func (err *stateMetadataError) As(target any) bool {
	if user, ok := target.(**proto.Error); ok {
		*user = err.wire
		return true
	}
	return false
}

func stateMetadataFailure(err error) error {
	if err == nil {
		return nil
	}
	var user *proto.Error
	message := strings.ReplaceAll(err.Error(), "\n", ". ")
	if errors.As(err, &user) {
		return &stateMetadataError{wire: UserError(user.Code, message, user.Hint), cause: err}
	}
	return &stateMetadataError{wire: UserError("state_metadata", "cannot update host state metadata: "+message, "Keep the reported files for inspection before retrying the host operation."), cause: err}
}

func stateMetadataPreserved(role stateMetadataRole, reason string) error {
	return UserError("state_metadata", "preserved "+string(role)+": "+reason, "Inspect the preserved state file and keep a copy before explicitly moving it aside, then retry.")
}

func validateStateMetadata(role stateMetadataRole, data []byte) error {
	if !validStateMetadataRole(role) {
		return placementError("the state metadata role is unknown")
	}
	if len(data) > proto.MaxFrame {
		return placementError("the state metadata exceeds the protocol bound")
	}
	if role == stateRestoreGate {
		return validateRestoreGateDataV1(data)
	}
	if role == stateLockdown {
		return validateLockdownDataV1(data)
	}
	if role == stateDrain {
		return validateDrainDataV1(data)
	}
	if role == stateFootprint {
		return validateFootprintLedgerV1(data)
	}
	var target any
	var cache wallCache
	var idle IdleState
	keys := []string{"boot_id", "version", "walls"}
	if role == stateWalls {
		target = &cache
	} else {
		target = &idle
		keys = []string{"boot_id", "busy_at"}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return placementError("the state metadata is malformed or contains unknown fields: " + err.Error())
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return placementError("the state metadata has trailing content")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || len(fields) != len(keys) {
		return placementError("the state metadata has missing or unknown fields")
	}
	for _, key := range keys {
		if _, exists := fields[key]; !exists {
			return placementError("the state metadata lacks a required field")
		}
	}
	if role == stateWalls && (cache.BootID == "" || cache.Version == "") || role == stateIdle && (idle.BootID == "" || idle.BusyAt.IsZero()) {
		return placementError("the state metadata has no boot, helper or activity identity")
	}
	return nil
}

func readStateMetadata(ctx context.Context, state *os.Root, role stateMetadataRole) ([]byte, os.FileInfo, error) {
	if !validStateMetadataRole(role) {
		return nil, nil, placementError("the state metadata role is unknown")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	before, err := metadataBefore(state, string(role))
	if err != nil || before == nil {
		return nil, before, err
	}
	file, err := cachefs.OpenFile(state, string(role), os.O_RDONLY, 0o600)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	var data []byte
	err = validateArchiveDescriptor(ctx, file, func(bytes []byte) error {
		data = bytes
		return validateStateMetadata(role, bytes)
	})
	if err != nil {
		return nil, nil, err
	}
	current, err := cachefs.ValidateFile(state, string(role), file, 0o600)
	if err != nil || !refOf(before).matches(current) {
		return nil, nil, placementError("the state metadata changed while its descriptor was read")
	}
	return data, current, nil
}

func checkStateMetadataRoot(control *placementControl, state *os.Root) error {
	if state == nil {
		return placementError("the state metadata root is missing")
	}
	if _, err := cachefs.CaptureRoot(state); err != nil {
		return err
	}
	if err := control.State.matchesRoot(state); err != nil {
		return err
	}
	return checkNamedPlacementRoot(state)
}

func checkStateMetadataGuard(control *placementControl, guard *os.Root) error {
	if guard == nil {
		return placementError("the state metadata guard is missing")
	}
	if _, err := cachefs.CaptureRoot(guard); err != nil {
		return err
	}
	if err := control.Control.matchesRoot(guard); err != nil {
		return err
	}
	return checkNamedPlacementRoot(guard)
}
