package client

import (
	"context"

	"github.com/iamlakshya/simstim/internal/api"
)

// LayerDescription is one applicable config source in config check's report.
type LayerDescription struct {
	Kind   api.LayerKind `json:"kind"`
	Name   string        `json:"name,omitempty"`
	Source string        `json:"source"`
}

// ConfigLayerReport describes the current folder's laptop layers.
func ConfigLayerReport() ([]LayerDescription, error) {
	settings, err := ReadPolicySettings()
	if err != nil {
		return nil, err
	}
	var h Host
	hosts, err := LoadHosts()
	if err != nil {
		return nil, err
	}
	defaultName, err := savedDefaultHost()
	if err != nil {
		return nil, err
	}
	for _, host := range hosts {
		if host.Name == defaultName {
			h = host
			break
		}
	}
	origin, _ := RepoOrigin(context.Background(), ".")
	layers, err := ResolvePolicyLayers(settings, h, origin, "", api.LayerValues{})
	if err != nil {
		return nil, err
	}
	var out []LayerDescription
	for _, l := range layers {
		if l.Kind == api.LayerSession {
			continue
		}
		source := "config.toml"
		if l.Kind == api.LayerHost {
			source = "hosts.toml"
		}
		name := l.Name
		if l.Kind == api.LayerHost && name != "" && name == defaultName {
			name += " (the default host)"
		}
		out = append(out, LayerDescription{Kind: l.Kind, Name: name, Source: source})
	}
	return out, nil
}
