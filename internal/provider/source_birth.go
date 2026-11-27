package provider

import "github.com/iamlakshya/simstim/internal/client"

// CreationSourceRequest projects the actual explicit provider request into the
// SAME protected creation reservation. It grants no proof or source authority.
func CreationSourceRequest(name string, r Request) client.HostCreationRequest {
	return client.HostCreationRequest{Name: r.Name, Provider: name, CPUs: r.CPUs, Memory: r.Memory, Disk: r.Disk, Image: r.Image, Template: r.Template, IdleStop: r.IdleStop, TokenCommand: r.TokenCommand}
}
