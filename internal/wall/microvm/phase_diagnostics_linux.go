//go:build linux

package microvm

import (
	"errors"
	"io"
)

func (phase *vmmExecPhaseV1) closeDiagnosticsV4() error {
	if phase == nil {
		return nil
	}
	phase.work.Lock()
	defer phase.work.Unlock()
	phase.mu.Lock()
	states := []*microVMFileRetirementV2{phase.parentRetirement, phase.diskRetirement, phase.readerRetirement, phase.writerRetirement}
	command, source := phase.command, phase.source
	phase.mu.Unlock()
	var diagnostic error
	for _, owner := range states {
		diagnostic = errors.Join(diagnostic, owner.diagnosticV2())
	}
	if command != nil && source != nil {
		diagnostic = errors.Join(diagnostic, command.VMMCloseDiagnosticsV4(source))
	}
	return diagnostic
}

// Caller holds lease.mu. Snapshot actual owners before custody is cleared;
// later retries with no phases preserve this SAME bounded report snapshot.
func (lease *bootRuntimeLeaseV1) capturePhaseDiagnosticsV4() {
	if len(lease.phases) == 0 {
		return
	}
	var diagnostic error
	for _, phase := range lease.phases {
		diagnostic = errors.Join(diagnostic, phase.closeDiagnosticsV4())
	}
	lease.phaseDiagnostic = diagnostic
}

func (lease *bootRuntimeLeaseV1) closeDiagnosticsV4() error {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	return errors.Join(lease.diagnostics, lease.phaseDiagnostic)
}

func (s *sandbox) captureRuntimeLeaseDiagnosticsV4(original io.Closer) {
	lease, ok := original.(*bootRuntimeLeaseV1)
	if !ok || lease == nil {
		return
	}
	diagnostic := lease.closeDiagnosticsV4()
	s.retirementMu.Lock()
	s.runtimeLeaseDiagnostic = diagnostic
	s.retirementMu.Unlock()
}
