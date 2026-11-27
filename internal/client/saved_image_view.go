package client

import (
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/image"
)

func cleanWallInfoV1(input *api.WallInfo) *api.WallInfo {
	if input == nil {
		return nil
	}
	result := *input
	result.Level, result.Driver, result.Version = CleanLine(input.Level), CleanLine(input.Driver), CleanLine(input.Version)
	result.Floor, result.DowngradedFrom, result.Preset = CleanLine(input.Floor), CleanLine(input.DowngradedFrom), CleanLine(input.Preset)
	result.SavedImage = nil
	if input.SavedImage != nil {
		original := input.SavedImage
		check := image.SavedHostV1{Version: original.Version, Ref: original.Ref, Digest: original.Digest, HostID: original.HostID}
		if check.Validate() == nil {
			copy := *original
			result.SavedImage = &copy
		}
	}
	return &result
}
