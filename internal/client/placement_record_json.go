package client

import (
	"bytes"
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/api"
)

// Pending is lifecycle data. JSON null must not turn an uncertain pending
// start into a completed record before a causal facts fence is captured.
func (record *placementRecordV1) UnmarshalJSON(raw []byte) error {
	type fields placementRecordV1
	var value fields
	if !api.ExactJSONFields(raw, value) {
		return placementStoreError(errPlacementState)
	}
	parsed, err := api.JSONObjectFields(raw, api.ExactJSONFieldNames(value)...)
	if err != nil {
		return placementStoreError(errPlacementState)
	}
	if pending, present := parsed["pending"]; present {
		token := bytes.TrimSpace(pending)
		if !bytes.Equal(token, []byte("true")) && !bytes.Equal(token, []byte("false")) {
			return placementStoreError(errPlacementState)
		}
	}
	if json.Unmarshal(raw, &value) != nil {
		return placementStoreError(errPlacementState)
	}
	*record = placementRecordV1(value)
	return nil
}
