package lima

import (
	"context"
	"errors"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/provider"
)

type limaSourceBirthKey struct{}
type limaSourceBirth struct {
	ctx        context.Context
	scope      *client.HostCreationSourceScope
	request    client.HostCreationRequest
	created    bool
	inspection *provider.SourceInspection
	result     client.Host
}

func init() {
	if err := client.RegisterHostMachineSourceObserver("lima", observeSourceBirth); err != nil {
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

func observeSourceBirth(ctx context.Context, scope *client.HostCreationSourceScope, result client.Host) (_ *client.MachineSourceProvenance, err error) {
	frame, _ := ctx.Value(limaSourceBirthKey{}).(*limaSourceBirth)
	if frame == nil || frame.ctx != ctx || scope == nil || scope != frame.scope || scope.Request() != frame.request || frame.request.Provider != "lima" || !frame.created || frame.inspection == nil || !client.SameEntry(frame.result, result) {
		return nil, plainSourceUnknown()
	}
	if err := scope.CheckContext(ctx); err != nil {
		return nil, err
	}
	if err := frame.inspection.Config.Revalidate(); err != nil {
		return nil, err
	}
	fresh, err := InspectSource(ctx, frame.result, nil)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, fresh.Close()) }()
	if fresh.Stamp != frame.inspection.Stamp || len(fresh.Paths) != 0 {
		return nil, plainSourceUnknown()
	}
	if err := frame.inspection.Config.Revalidate(); err != nil {
		return nil, err
	}
	if err := scope.CheckContext(ctx); err != nil {
		return nil, err
	}
	return client.CloneMachineSource(frame.result.Machine.Source), nil
}
