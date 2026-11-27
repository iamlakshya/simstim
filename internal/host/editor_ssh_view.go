package host

import (
	"encoding/json"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func liveEditorSSHViewV1(info *SessionInfo) {
	info.SSH = false
	if !info.Alive || info.Wall == nil {
		return
	}
	raw, err := HolderRequestWithFeatures(info.ID, "ssh.view", struct{}{}, 500*time.Millisecond, proto.FeatureEditorSSHV1)
	if err != nil {
		return
	}
	var view proto.EditorSSHViewV1
	if !api.ExactJSONFields(raw, view) || json.Unmarshal(raw, &view) != nil || view.Version != 1 {
		return
	}
	info.SSH = view.Open
}
