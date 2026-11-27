package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
)

type restoreGateDataV1 struct {
	Version int    `json:"version"`
	HostID  string `json:"host_id"`
	UID     int    `json:"uid"`
}

func validateRestoreGateDataV1(raw []byte) error {
	var data restoreGateDataV1
	fields, err := api.JSONObjectFields(raw, "version", "host_id", "uid")
	if err != nil || len(fields) != 3 || bytes.Equal(bytes.TrimSpace(fields["uid"]), []byte("null")) || !api.ExactJSONFields(raw, data) || json.Unmarshal(raw, &data) != nil || data.Version != 1 || !ValidHostID(data.HostID) || data.UID < 0 {
		return placementError("the restore coordinator has an unknown schema or owner")
	}
	return nil
}

// restoreGateV1 serialises restore flows across helpers. Its original locked
// FD and publisher outlive the workers. It supplies no execution, source,
// session operation, stop, artifact or capacity authority.
type restoreGateV1 struct {
	publisher *stateMetadataPublisher
	state     *os.Root
	file      *os.File
	reference cachefs.Reference
}

func beginRestoreGateV1(ctx context.Context) (_ *restoreGateV1, returned error) {
	publisher, err := beginStateMetadataPublisher(ctx)
	if err != nil {
		return nil, err
	}
	gate := &restoreGateV1{publisher: publisher}
	complete := false
	defer func() {
		if !complete {
			returned = errors.Join(returned, gate.Close())
		}
	}()
	err = withPlacementRoot(publisher.ctx, false, func(control *placementControl, guard *os.Root) error {
		if err := checkStateMetadataPublisher(publisher.ctx, control, publisher.operation); err != nil {
			return err
		}
		gate.state, err = cachefs.OpenPrivateRoot(paths.StateDir(), false)
		if err != nil {
			return err
		}
		if err := checkStateMetadataRoot(control, gate.state); err != nil {
			return err
		}
		if err := replayStateMetadata(publisher.ctx, guard, control, gate.state, stateRestoreGate); err != nil {
			return err
		}
		if err := gate.openOriginalV1(control, guard); err != nil {
			return err
		}
		if err := gate.lockOriginalV1(); err != nil {
			return err
		}
		return gate.Check()
	}, true)
	if err != nil {
		return nil, stateMetadataFailure(err)
	}
	complete = true
	return gate, nil
}

func (gate *restoreGateV1) lockOriginalV1() error {
	if err := gate.reference.CheckFile(gate.state, string(stateRestoreGate), gate.file, 0o600); err != nil {
		return stateMetadataPreserved(stateRestoreGate, "the original coordinator changed before its native lock")
	}
	held, err := cachefs.TryLock(gate.file, cachefs.Exclusive)
	if err != nil {
		return err
	}
	if !held {
		return UserError("busy", "another restore is already running on this host", "Wait for it to finish, then retry host restore.")
	}
	return gate.reference.CheckFile(gate.state, string(stateRestoreGate), gate.file, 0o600)
}

func (gate *restoreGateV1) openOriginalV1(control *placementControl, guard *os.Root) error {
	raw, before, err := readStateMetadata(gate.publisher.ctx, gate.state, stateRestoreGate)
	if err != nil {
		return err
	}
	key := "state/" + string(stateRestoreGate)
	expected, recorded := control.Refs[key]
	if before == nil {
		if recorded {
			return stateMetadataPreserved(stateRestoreGate, "the original coordinator is missing; its native lock must not be replaced")
		}
		raw, err = json.Marshal(restoreGateDataV1{Version: 1, HostID: control.HostID, UID: control.UID})
		if err != nil {
			return err
		}
		after, err := writeOwnedMetadataIntent(gate.state, string(stateRestoreGate), append(raw, '\n'), nil, func(info os.FileInfo, temporary string) error {
			return prepareStateMetadataWrite(gate.publisher.ctx, guard, control, gate.state, stateRestoreGate, nil, info, temporary)
		})
		if err != nil {
			return err
		}
		if control.Refs == nil {
			control.Refs = make(map[string]purgeRef)
		}
		expected = refOf(after)
		control.Refs[key] = expected
		delete(control.StateWrites, string(stateRestoreGate))
	} else if !recorded || !expected.matches(before) {
		return stateMetadataPreserved(stateRestoreGate, "the coordinator has no matching original producer receipt")
	}
	var data restoreGateDataV1
	if err := json.Unmarshal(raw, &data); err != nil || data.HostID != control.HostID || data.UID != control.UID {
		return stateMetadataPreserved(stateRestoreGate, "the original coordinator belongs to another host or user")
	}
	gate.file, err = cachefs.OpenFile(gate.state, string(stateRestoreGate), os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	actual, err := cachefs.ValidateFile(gate.state, string(stateRestoreGate), gate.file, 0o600)
	if err != nil || !expected.matches(actual) {
		return stateMetadataPreserved(stateRestoreGate, "the original coordinator changed before native lock admission")
	}
	gate.reference, err = cachefs.CaptureFile(gate.state, string(stateRestoreGate), gate.file, 0o600)
	return err
}

// Check repeats this coordinator's context, root and descriptor checks. It
// does not establish a sandbox's physical exit or supply start authority.
func (gate *restoreGateV1) Check() error {
	if gate == nil || gate.publisher == nil || gate.publisher.ctx == nil || gate.publisher.operation == nil || gate.publisher.operation.owner == nil || gate.state == nil || gate.file == nil {
		return placementError("the restore coordinator has no original native owner")
	}
	return errors.Join(context.Cause(gate.publisher.ctx), gate.publisher.operation.owner.checkIdentity(), gate.reference.CheckFile(gate.state, string(stateRestoreGate), gate.file, 0o600))
}

// Close releases the coordinator after its caller has joined all restore
// workers and their owned cancellation cleanup.
func (gate *restoreGateV1) Close() error {
	if gate == nil {
		return nil
	}
	var result error
	if gate.file != nil {
		result = errors.Join(result, gate.file.Close())
		gate.file = nil
	}
	if gate.state != nil {
		result = errors.Join(result, gate.state.Close())
		gate.state = nil
	}
	if gate.publisher != nil {
		result = errors.Join(result, gate.publisher.close())
	}
	return result
}
