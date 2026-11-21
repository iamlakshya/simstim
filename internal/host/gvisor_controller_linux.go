//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"path"
	"runtime"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/jail"
	"github.com/iamlakshya/simstim/internal/wall"
	"github.com/iamlakshya/simstim/internal/wall/gvisor"
	"golang.org/x/sys/unix"
)

// Called only after commandGvisorOriginalV1 has joined the same protected
// runtime, original request/operation, control roots and actual mount objects.
func constructOriginalGvisorControllerV1(ctx context.Context, request gvisor.CommandRequest, spec wall.Spec, commandSpec jail.Spec, bootstrap *jail.GvisorBootstrapV1) (_ *jail.Cmd, failureErr error) {
	if len(commandSpec.Files) != 1 || commandSpec.Files[0] == nil {
		return nil, vmNeedError("the original controller has no exact outside guest stream")
	}
	manifest, err := gvisorExpectedRuntimeV1(ctx)
	if err != nil {
		return nil, err
	}
	code, err := request.Suite.RetainControllerCodeV1(ctx, manifest)
	if err != nil {
		return nil, err
	}
	defer func() { failureErr = errors.Join(failureErr, code.Close()) }()
	topology, err := request.Topology.RetainPublicV1()
	if err != nil {
		return nil, err
	}
	defer func() { failureErr = errors.Join(failureErr, topology.Close()) }()
	if topology.ID != spec.ID || spec.MemoryMax <= 0 {
		return nil, vmNeedError("the original controller lacks its protected resource or session projection")
	}
	var memory unix.Sysinfo_t
	if err := unix.Sysinfo(&memory); err != nil {
		return nil, err
	}
	unit := uint64(memory.Unit)
	total := uint64(memory.Totalram)
	if unit == 0 || total == 0 || total > (1<<60)/unit {
		return nil, vmNeedError("the original controller has no bounded actual host memory value")
	}
	cpus := spec.CPUs
	if cpus == 0 {
		cpus = runtime.NumCPU()
	}
	if cpus < 1 || cpus > 256 {
		return nil, vmNeedError("the original controller CPU projection exceeds its fixed range")
	}
	input := jail.GvisorControllerInputV1{ID: topology.ID, Role: topology.Role, CPUs: cpus, MemoryMax: uint64(spec.MemoryMax), HostMemory: total * unit, Topology: topology.Descriptor, Sources: topology.Sources, Runsc: code.Runsc, ProcUmounter: code.ProcUmounter, Sentry: code.Sentry, FDParking: code.FDParking, Prewarmer: code.Prewarmer}
	if topology.Role == "guest-init" {
		helper, err := os.Open("/proc/self/exe")
		if err != nil {
			return nil, err
		}
		defer func() { failureErr = errors.Join(failureErr, helper.Close()) }()
		input.Helper, input.HelperGuest = helper, path.Join("/opt", brand.Name, "bin", brand.HostBinary)
	} else if topology.Role != "probe-true" {
		return nil, vmNeedError("the original controller has another fixed guest role")
	}
	// The original guest is NEVER in Spec.Files or any inherited C vector.
	guest := commandSpec.Files[0]
	commandSpec.Argv, commandSpec.Env, commandSpec.Files = nil, nil, nil
	return jail.CommandGvisorControllerCapturedV1(ctx, commandSpec, bootstrap, input, guest)
}

func startOriginalGvisorControllerV1(command *originalGvisorCommandV1) error {
	if err := command.Cmd.Start(); err != nil {
		return err
	}
	aggregate, err := command.Cmd.AwaitGvisorControllerV1(command.ctx)
	if err == nil {
		operation := command.receipt.boot.operation
		err = operation.withCurrentRead(command.ctx, func(owned context.Context) error {
			receipt, err := checkOriginalGvisorSpecV1(owned, command.spec)
			if err != nil {
				return err
			}
			if receipt != command.receipt || receipt.boot.operation != operation {
				return vmNeedError("the original gvisor operation changed before private guest release")
			}
			owner := receipt.boot.owner
			owner.mu.Lock()
			valid := owner.startup == receipt.boot && !receipt.boot.ready && receipt.boot.gvisorIssued && operation.Valid(receipt.boot.loan.Primary)
			owner.mu.Unlock()
			if !valid {
				return vmNeedError("the original gvisor boot owner ended before private guest release")
			}
			manifest, err := gvisorExpectedRuntimeV1(owned)
			if err != nil {
				return err
			}
			if err := command.suite.CheckOriginalV1(owned, manifest, nil); err != nil {
				return err
			}
			return aggregate.ReleaseGuestV1(owned)
		})
	}
	if err == nil {
		return nil
	}
	command.cancel()
	return errors.Join(err, command.Cmd.Signal(os.Kill), command.Cmd.Wait())
}

func startProbeGvisorControllerV1(command *probeGvisorCommandV1) error {
	if err := command.Cmd.Start(); err != nil {
		return err
	}
	aggregate, err := command.Cmd.AwaitGvisorControllerV1(command.ctx)
	if err == nil {
		err = command.purpose.check(command.ctx)
	}
	if err == nil {
		manifest, expectedErr := gvisorExpectedRuntimeV1(command.ctx)
		err = expectedErr
		if err == nil {
			err = command.suite.CheckOriginalV1(command.ctx, manifest, nil)
		}
	}
	if err == nil {
		purpose := command.purpose
		purpose.mu.Lock()
		if purpose.closed || !purpose.issued || purpose.command != command {
			err = newRuntimeError("the original physical probe ended before private guest release")
		} else {
			err = aggregate.ReleaseGuestV1(command.ctx)
		}
		purpose.mu.Unlock()
	}
	if err == nil {
		return nil
	}
	command.cancel()
	return errors.Join(err, command.Cmd.Signal(os.Kill), command.Wait())
}
