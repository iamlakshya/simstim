package api

import (
	"encoding/json"
	"errors"
	"slices"
)

// ErrModelUsageV1 marks an unsupported or inconsistent accounting snapshot.
var ErrModelUsageV1 = errors.New("invalid model usage snapshot")

// CloneModelUsageV1 owns every mutable value in a display snapshot.
func CloneModelUsageV1(input *ModelUsageV1) *ModelUsageV1 {
	if input == nil {
		return nil
	}
	copy := *input
	copy.Models = slices.Clone(input.Models)
	return &copy
}

// UnmarshalJSON closes V1 accounting fields before numeric invariants are
// checked. Unknown/aliased/duplicate metadata cannot become a trusted total.
func (snapshot *ModelUsageV1) UnmarshalJSON(raw []byte) error {
	type wire ModelUsageV1
	if !ExactJSONFields(raw, wire{}) {
		return ErrModelUsageV1
	}
	var shape struct {
		Total        json.RawMessage   `json:"total"`
		Models       []json.RawMessage `json:"models"`
		Unattributed json.RawMessage   `json:"unattributed"`
	}
	if json.Unmarshal(raw, &shape) != nil || len(shape.Models) > ModelUsageModelLimitV1 || !ExactJSONFields(shape.Total, ModelUsageCountsV1{}) || !ExactJSONFields(shape.Unattributed, ModelUsageCountsV1{}) {
		return ErrModelUsageV1
	}
	for _, model := range shape.Models {
		if !ExactJSONFields(model, ModelUsageModelV1{}) {
			return ErrModelUsageV1
		}
		var entry struct {
			Counts json.RawMessage `json:"counts"`
		}
		if json.Unmarshal(model, &entry) != nil || !ExactJSONFields(entry.Counts, ModelUsageCountsV1{}) {
			return ErrModelUsageV1
		}
	}
	var value wire
	if json.Unmarshal(raw, &value) != nil || !ValidModelUsageV1(ModelUsageV1(value)) {
		return ErrModelUsageV1
	}
	*snapshot = ModelUsageV1(value)
	return nil
}
