//go:build linux

package microvm

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/runtimecache"
	"github.com/iamlakshya/simstim/internal/supply"
)

// bootRuntimeLeaseV1 is the original physical resource owner. An interrupted
// mode phase settles through its SAME Cmd before any runtime or image lease
// is released. Failed proof retains the complete stack for a later retry.
type bootRuntimeLeaseV1 struct {
	mu              sync.Mutex
	agents          io.Closer
	runtime         []*supply.VerifiedRuntime
	phases          []*vmmExecPhaseV1
	image           io.Closer
	runtimeStore    *runtimecache.Store
	imageStore      *image.Store
	runtimeFiles    []*os.File
	retirements     map[*os.File]*microVMFileRetirementV2
	diagnostics     error
	phaseDiagnostic error
}

func (lease *bootRuntimeLeaseV1) Close() error {
	if lease == nil {
		return nil
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	defer lease.capturePhaseDiagnosticsV4()
	for _, phase := range lease.phases {
		if err := phase.settleNoFuture(context.WithoutCancel(phase.ctx)); err != nil {
			return err
		}
	}
	lease.capturePhaseDiagnosticsV4()
	lease.phases = nil
	if lease.retirements == nil {
		lease.retirements = map[*os.File]*microVMFileRetirementV2{}
	}
	if err := closeMicroVMRuntimeOwnersV2(&lease.runtime, &lease.runtimeFiles, lease.retirements, &lease.diagnostics); err != nil {
		return err
	}
	if lease.image != nil {
		if err := lease.image.Close(); err != nil {
			return err
		}
		lease.image = nil
	}
	if lease.agents != nil {
		if err := lease.agents.Close(); err != nil {
			return err
		}
		lease.agents = nil
	}
	if lease.imageStore != nil {
		if err := lease.imageStore.Close(); err != nil {
			return err
		}
		lease.imageStore = nil
	}
	if lease.runtimeStore != nil {
		if err := lease.runtimeStore.Close(); err != nil {
			return err
		}
		lease.runtimeStore = nil
	}
	return nil
}
func (s *sandbox) retainVMMModePhaseV1(phase *vmmExecPhaseV1) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	lease, ok := s.agentsLease.(*bootRuntimeLeaseV1)
	if !ok || lease == nil {
		return errors.New("the original VMM mode phase lost its physical resource owner")
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	lease.phases = append(lease.phases, phase)
	return nil
}
