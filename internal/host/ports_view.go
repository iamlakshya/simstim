package host

import (
	"encoding/json"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func livePortView(info *SessionInfo) {
	if info.Wall == nil || !info.Alive {
		return
	}
	raw, err := HolderRequestWithFeatures(info.ID, "ports.view", struct{}{}, 500*time.Millisecond, api.FeatureSandboxPortsV1)
	if err != nil {
		return
	}
	var view api.PortForwardViewV1
	if !api.ExactJSONFields(raw, view) || json.Unmarshal(raw, &view) != nil || view.Version != 1 || len(view.Forwards) > 64 {
		return
	}
	seen := map[api.PortForwardInfo]bool{}
	for _, forward := range view.Forwards {
		if forward.Port < 1 || forward.Port > 65535 || seen[forward] {
			return
		}
		seen[forward] = true
	}
	info.Forwards = view.Forwards
}
