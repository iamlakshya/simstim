package client

import (
	"github.com/iamlakshya/simstim/internal/runtimecache"
	"github.com/iamlakshya/simstim/internal/supply"
)

// runtimeManifest keeps the existing runtime-file-only selection contract.
// Retention metadata travels separately and never widens the file roles.
func runtimeManifest(arch string) (supply.Manifest, error) {
	request, err := runtimeRequestFor(arch)
	return request.Manifest, err
}

func runtimeRequestFor(arch string) (runtimecache.RuntimeRequest, error) {
	m, err := ReleaseManifest()
	if err != nil {
		return runtimecache.RuntimeRequest{}, err
	}
	request := runtimecache.RuntimeRequest{Manifest: supply.Manifest{Release: m.Release, Runtime: m.Runtime, Advisories: supply.MergeAdvisories(m.Advisories, supply.KnownBad())}}
	defaults := []supply.Item{}
	hasRuntime := false
	for _, item := range m.Items {
		if item.Kind == supply.KindImage && item.Arch == arch {
			defaults = append(defaults, item)
		}
		if runtimecache.IsRuntime(item.Kind) {
			hasRuntime = true
			if item.Arch == arch {
				request.Manifest.Items = append(request.Manifest.Items, item)
			}
		}
	}
	request.ImageDefaults = &defaults
	if !hasRuntime {
		return request, nil
	}
	if err := runtimecache.ValidateWithImageDefaults(request.Manifest, request.ImageDefaults, arch); err != nil {
		return request, &UserError{Msg: "this client's runtime manifest is invalid: " + CleanLine(err.Error()), Hint: "Install a release of the client for this host architecture.", Code: "manifest_damaged"}
	}
	return request, nil
}
