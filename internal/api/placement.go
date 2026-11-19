package api

import (
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/proto"
)

const PlacementVersion = 1

// PlacementV1 records why the original client selected its host. It supplies
// no Need, capacity reservation, native source, operation or start permission.
type PlacementV1 struct {
	Version  int    `json:"v"`
	PlacedBy string `json:"placed_by"`
	Reason   string `json:"reason"`
}

func (p PlacementV1) Validate() error {
	if p.Version != PlacementVersion || p.PlacedBy != "auto" && p.PlacedBy != "explicit" || p.Reason == "" || CleanText(p.Reason, 256) != p.Reason {
		return placementMetadataError()
	}
	return nil
}

func (p *PlacementV1) UnmarshalJSON(raw []byte) error {
	type wire PlacementV1
	var value wire
	if !ExactJSONFields(raw, value) || json.Unmarshal(raw, &value) != nil {
		return placementMetadataError()
	}
	*p = PlacementV1(value)
	return p.Validate()
}

func placementMetadataError() error {
	return &proto.Error{Code: "bad_placement", Message: "the placement description is not valid versioned metadata",
		Hint: "Use clean placement metadata from the original host-selection request."}
}

// ClonePlacementV1 owns the optional advisory record without changing it.
func ClonePlacementV1(original *PlacementV1) *PlacementV1 {
	if original == nil {
		return nil
	}
	copy := *original
	return &copy
}
