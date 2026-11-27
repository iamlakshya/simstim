package host

import (
	"encoding/json"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// livePackageSettings projects only the actual installed start. Saved policy
// can change while a guest keeps its old files, so it cannot supply this view.
func livePackageSettings(info *SessionInfo) {
	info.PackageManagers = nil
	if info.Wall == nil || !info.Alive {
		return
	}
	raw, err := HolderRequestWithFeatures(info.ID, "packages.view", struct{}{}, 500*time.Millisecond, proto.FeaturePackageManagersV1)
	if err != nil {
		return
	}
	plan, err := decodePackageSettingsView(raw)
	if err == nil {
		info.PackageManagers = plan
	}
}

func decodePackageSettingsView(raw []byte) (*proto.PackageManagersV1, error) {
	var plan *proto.PackageManagersV1
	if !api.ExactJSONFields(raw, proto.PackageManagersV1{}) || json.Unmarshal(raw, &plan) != nil {
		return nil, UserError("wall_setup", "the installed package settings reply is invalid", "Stop and resume the sandbox with matching helpers.")
	}
	if err := proto.ValidatePackageManagersV1(plan); err != nil {
		return nil, err
	}
	return proto.ClonePackageManagersV1(plan), nil
}
