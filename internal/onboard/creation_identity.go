package onboard

import (
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/provider"
)

// CreationRequest copies the actual provider request before any external
// work. The progress writer grants no ownership and remains with the caller.
func CreationRequest(p provider.Provider, req provider.Request) client.HostCreationRequest {
	return client.HostCreationRequest{Name: req.Name, Provider: p.Name(), CPUs: req.CPUs,
		Memory: req.Memory, Disk: req.Disk, Image: req.Image,
		Template: req.Template, IdleStop: req.IdleStop, TokenCommand: req.TokenCommand}
}
