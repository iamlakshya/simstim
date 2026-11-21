package host

import (
	"encoding/json"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

// Live rows read the same current root actor; no manifest or historical DTO
// can fabricate a zero meter, per-member attribution or budget authority.
func liveModelUsage(info *SessionInfo) {
	info.Usage = nil
	if info.Wall == nil || !info.Alive {
		return
	}
	raw, err := HolderRequestWithFeatures(info.ID, "model.usage", struct{}{}, 500*time.Millisecond, api.FeatureModelUsageV1)
	if err != nil {
		return
	}
	var usage api.ModelUsageV1
	if !api.ExactJSONFields(raw, usage) || json.Unmarshal(raw, &usage) != nil || !api.ValidModelUsageV1(usage) {
		return
	}
	info.Usage = api.CloneModelUsageV1(&usage)
}
