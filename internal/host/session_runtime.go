package host

import (
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/runtimecache"
)

func sessionRuntimeInfoV1(manifest *Manifest) *api.SessionRuntimeInfoV1 {
	if manifest.RuntimeManifest == nil || manifest.SandboxOf != "" || manifest.Wall == nil {
		return nil
	}
	result := &api.SessionRuntimeInfoV1{Version: 1, Label: manifest.RuntimeManifest.Runtime, Items: []api.SessionRuntimeItemV1{}}
	for _, item := range manifest.RuntimeManifest.Items {
		if runtimecache.IsRuntime(item.Kind) {
			result.Items = append(result.Items, api.SessionRuntimeItemV1{Kind: item.Kind, Name: item.Name, Arch: item.Arch, Version: item.Version, SHA256: item.SHA256})
		}
	}
	return result
}
