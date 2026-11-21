//go:build linux

package host

import "errors"

// This exposes only the original cleanup owners' report data. It cannot issue
// a Cold, None, stopped, boot, role, native-retirement or execution permission.
func (cold *ColdResumeV1) CloseDiagnosticsV4() error {
	if cold == nil {
		return nil
	}
	var diagnostic error
	if cold.vacancy != nil {
		diagnostic = errors.Join(diagnostic, cold.vacancy.CloseDiagnosticsV4())
	}
	if cold.startup != nil {
		diagnostic = errors.Join(diagnostic, cold.startup.ManagerCloseDiagnosticsV4())
	}
	if cold.custody != nil && cold.custody.custodyKernelSourceV1 != nil {
		diagnostic = errors.Join(diagnostic, cold.custody.managerDiagnostic)
	}
	return diagnostic
}
