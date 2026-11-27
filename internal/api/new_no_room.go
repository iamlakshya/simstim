package api

import (
	"encoding/json"
	"fmt"
	"strings"
)

const FeatureNewNoRoomSettlementV1 = "new.no-room-settlement-v1"

// NewNoRoomSettlementV1 is a helper's bounded control report. Decoding one
// creates no client retirement authority. The actual pending request must bind
// its complete reply to the original request, connection and local owners.
type NewNoRoomSettlementV1 struct {
	Version   int    `json:"v"`
	RequestID uint64 `json:"request_id"`
	SessionID string `json:"session_id"`
	HostID    string `json:"host_id"`
	Attempt   string `json:"attempt"`
	Outcome   string `json:"outcome"`
}

// NewNoRoomEnvelopeV1 is transient RPC control data. It never belongs in a
// manifest, receipt, grant audit or a public diagnostic. Room is diagnostics.
type NewNoRoomEnvelopeV1 struct {
	Settlement NewNoRoomSettlementV1 `json:"new_settlement"`
	Room       json.RawMessage       `json:"room,omitempty"`
}

func ValidNewAttemptNonceV1(value string) bool {
	return len(value) == 32 && strings.Trim(value, "0123456789abcdef") == ""
}

func (s NewNoRoomSettlementV1) Validate() error {
	if s.Version != 1 || s.RequestID == 0 || !ValidSessionID(s.SessionID) ||
		!ValidNewAttemptNonceV1(s.HostID) || !ValidNewAttemptNonceV1(s.Attempt) || s.Outcome != "never-dispatched" {
		return fmt.Errorf("the New refusal settlement is invalid. Try: keep the original attempt and update the helper")
	}
	return nil
}

func DecodeNewNoRoomEnvelopeV1(raw []byte) (NewNoRoomEnvelopeV1, error) {
	var envelope NewNoRoomEnvelopeV1
	if len(raw) > 8192 || !ExactJSONFields(raw, envelope) || json.Unmarshal(raw, &envelope) != nil {
		return envelope, fmt.Errorf("the New refusal envelope is invalid. Try: keep the original attempt and update the helper")
	}
	fields, err := JSONObjectFields(raw, "new_settlement", "room")
	if err != nil || !ExactJSONFields(fields["new_settlement"], envelope.Settlement) {
		return envelope, fmt.Errorf("the New refusal envelope is invalid. Try: keep the original attempt and update the helper")
	}
	return envelope, envelope.Settlement.Validate()
}
