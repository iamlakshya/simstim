package host

import (
	"encoding/json"
	"time"
)

type idleShutdownCommit struct {
	BootID string                   `json:"boot_id"`
	Nonce  string                   `json:"nonce"`
	Owner  placementOperationRecord `json:"owner"`
	At     time.Time                `json:"at"`

	extra      map[string]json.RawMessage
	ownerExtra map[string]json.RawMessage
}

func knownIdleShutdownBoot(boot string) bool {
	if len(boot) != 36 {
		return false
	}
	for index, value := range []byte(boot) {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if value != '-' {
				return false
			}
		} else if !(value >= '0' && value <= '9' || value >= 'a' && value <= 'f') {
			return false
		}
	}
	return true
}

func validateIdleShutdownCommit(marker *idleShutdownCommit) error {
	if marker == nil {
		return nil
	}
	if !knownIdleShutdownBoot(marker.BootID) || !ValidHostID(marker.Nonce) || marker.Owner.PID <= 0 || marker.Owner.Birth == "" || marker.Owner.Kind != "publish" || marker.Owner.At.IsZero() || marker.Owner.extra != "" || marker.At.IsZero() || len(marker.extra) != 0 || len(marker.ownerExtra) != 0 {
		return placementError("the committed idle shutdown has unknown identity or fields")
	}
	return nil
}

func reconcileIdleShutdownCommit(control *placementControl, boot string) error {
	marker := control.ShutdownCommitted
	if marker == nil {
		return nil
	}
	if err := validateIdleShutdownCommit(marker); err != nil {
		return err
	}
	if !knownIdleShutdownBoot(boot) {
		return placementError("cannot establish this boot while an idle shutdown may already be queued")
	}
	if marker.BootID == boot {
		return UserError("shutdown_committed", "this boot already committed an idle shutdown and it may be queued", "Wait for the host's state to settle and inspect it before retrying. A queued shutdown cannot be recalled by stopping its timer.")
	}
	control.ShutdownCommitted = nil
	return nil
}

func (marker *idleShutdownCommit) UnmarshalJSON(data []byte) error {
	type plain idleShutdownCommit
	var value plain
	extra, err := decodeFootprint(data, &value, []string{"boot_id", "nonce", "owner", "at"})
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	ownerExtra, err := decodeFootprint(fields["owner"], &value.Owner, []string{"pid", "birth", "kind", "at"})
	*marker = idleShutdownCommit(value)
	marker.extra, marker.ownerExtra = extra, ownerExtra
	return err
}

func (marker idleShutdownCommit) MarshalJSON() ([]byte, error) {
	type plain idleShutdownCommit
	data, err := encodeFootprint(plain(marker), marker.extra)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	owner, err := encodeFootprint(marker.Owner, marker.ownerExtra)
	if err != nil {
		return nil, err
	}
	fields["owner"] = owner
	return json.Marshal(fields)
}
