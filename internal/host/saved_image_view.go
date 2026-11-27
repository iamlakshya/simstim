package host

import "github.com/iamlakshya/simstim/internal/api"

func wallInfoForManifestV1(manifest *Manifest) *api.WallInfo {
	if manifest == nil {
		return nil
	}
	result := wallInfo(manifest.Wall)
	if result == nil || manifest.Wall.Level != "microvm" {
		return result
	}
	// Read only the protected immutable Need through its canonical codec. A
	// guest claim, mutable status, Run.Image or inventory cannot supply origin.
	need, known, err := protectedNeedReferences(*manifest)
	if err != nil || !known || need.Image.SavedHost == nil {
		return result
	}
	original := need.Image.SavedHost
	result.SavedImage = &api.SavedImageInfoV1{Version: original.Version, Ref: original.Ref, Digest: original.Digest, HostID: original.HostID}
	return result
}
