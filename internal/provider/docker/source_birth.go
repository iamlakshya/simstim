package docker

import (
	"context"
	"github.com/iamlakshya/simstim/internal/client"
)

type dockerSourceBirthKey struct{}
type dockerSourceBirth struct {
	ctx       context.Context
	scope     *client.HostCreationSourceScope
	request   client.HostCreationRequest
	locator   *dockerSourceLocator
	runID     string
	result    client.Host
	attempted bool
	partial   client.Host
	metadata  *client.ProviderSourceMetadata
}

func init() {
	if err := client.RegisterHostMachineSourceObserver("docker", observeSourceBirth); err != nil {
		panic(err)
	}
}

func freezeBirthHost(h client.Host) client.Host {
	if h.Machine != nil {
		machine := *h.Machine
		machine.Source = client.CloneMachineSource(machine.Source)
		h.Machine = &machine
	}
	return h
}

func observeSourceBirth(ctx context.Context, scope *client.HostCreationSourceScope, result client.Host) (*client.MachineSourceProvenance, error) {
	frame, _ := ctx.Value(dockerSourceBirthKey{}).(*dockerSourceBirth)
	if frame == nil || frame.ctx != ctx || scope == nil || scope != frame.scope || scope.Request() != frame.request || frame.request.Provider != "docker" || frame.locator == nil || frame.runID == "" || !client.SameEntry(frame.result, result) {
		return nil, sourceUnknown()
	}
	if err := scope.CheckContext(ctx); err != nil {
		return nil, err
	}
	if err := frame.metadata.Revalidate(ctx); err != nil {
		return nil, err
	}
	if err := frame.locator.pin.Revalidate(); err != nil {
		return nil, err
	}
	source, err := frame.locator.receipt(ctx, result, frame.runID)
	if err != nil {
		return nil, err
	}
	if err := frame.locator.pin.Revalidate(); err != nil {
		return nil, err
	}
	if err := scope.CheckContext(ctx); err != nil {
		return nil, err
	}
	return client.CloneMachineSource(source), nil
}
