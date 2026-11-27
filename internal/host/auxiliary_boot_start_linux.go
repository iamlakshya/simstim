//go:build linux

package host

import (
	"context"
	"errors"
	"net"

	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/vmconfig"
	"github.com/iamlakshya/simstim/internal/wall/microvm"
)

// startAuxiliaryGuestV1 is the only actual Start consumer of this closed
// source. A raw saved Need or ExpectedSpec cannot enter this method without
// the adopted original OP/holder pair and NEW native capped helper identity.
func (boot *AuxiliaryBootV1) startAuxiliaryGuestV1(ctx context.Context) (_ *auxiliaryTransportV1, returned error) {
	engine, err := boot.build(ctx)
	if err != nil {
		return nil, err
	}
	boot.prepareMu.Lock()
	boot.started = true
	boot.prepareMu.Unlock()
	connection, err := engine.Start(boot.Context())
	if err != nil {
		return nil, err
	}
	producer, ok := engine.(interface {
		NativeBindingV1(context.Context, net.Conn) (*microvm.NativeBindingV1, error)
	})
	if !ok {
		return nil, auxiliaryUnavailableV1("the actual auxiliary runtime has no native engine binding")
	}
	binding, err := producer.NativeBindingV1(ctx, connection)
	if err != nil {
		return nil, err
	}
	correlation, err := boot.prepareCorrelation(ctx, binding, connection)
	if err != nil {
		return nil, err
	}
	session := link.New(connection, link.Supervisor, link.Options{})
	complete := false
	defer func() {
		if !complete {
			returned = errors.Join(returned, session.Close())
		}
	}()
	transport, err := newAuxiliaryTransportV1(ctx, session)
	if err != nil {
		return nil, err
	}
	config := proto.VMStartV1{Version: 1, System: "reset", StateDirs: []string{}}
	if boot.manifest.Policy != nil && boot.manifest.Policy.Sandbox.System != nil {
		config.System = *boot.manifest.Policy.Sandbox.System
	}
	// No state capture, default agent, helper setup, hooks, package writes,
	// egress service, values or client delivery is installed for this boot.
	plan, err := vmconfig.Validate(proto.CtlVMPrepareArgs{Session: boot.candidate.primary, VM: config, StateFiles: []string{}, Correlation: correlation})
	if err != nil {
		return nil, err
	}
	var receipt proto.CtlVMPrepareResult
	if err := transport.Call(ctx, proto.CtlVMPrepare, plan.Args, &receipt); err != nil {
		return nil, err
	}
	if receipt.GuestABI != proto.GuestOwnedPTYV2 {
		return nil, auxiliaryUnavailableV1("the auxiliary guest lacks its actual owned init ABI")
	}
	if err := boot.ConfirmPrepared(ctx, connection, receipt.Correlation); err != nil {
		return nil, err
	}
	complete = true
	return transport, nil
}
