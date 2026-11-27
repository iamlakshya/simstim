package tui

import (
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// startInputOptions retains only the original frontend choices. The client's
// same request selects the actual settings, repository, sources and captures.
// The frontend never supplies a source witness or input plan.
func startInputOptions(a client.NewArgs, hostClone bool) client.NewInputOptions {
	values := api.LayerValues{Sandbox: api.SandboxValues{Mounts: append([]string(nil), a.Mounts...), Include: append([]string(nil), a.Include...)}}
	if a.Wall != "" {
		wall := a.Wall
		values.Sandbox.Wall = &wall
	}
	options := client.NewInputOptions{HostClone: hostClone, Session: values}
	for _, layer := range a.Layers {
		if layer.Kind == api.LayerSession {
			options.Session = api.CloneLayerValues(layer.LayerValues)
		}
		if layer.Kind == api.LayerProfile {
			options.Profile = layer.Name
		}
	}
	return options
}
