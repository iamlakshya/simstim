//go:build linux

package host

import "errors"

// These are retained original I/O reports. Native Close alone controls
// progress and every actual descriptor still follows its first close facts.
func (main *ManagerCustodyMainV1) CloseDiagnosticsV4() error {
	if main == nil {
		return nil
	}
	main.mu.Lock()
	defer main.mu.Unlock()
	return errors.Join(main.managerDiagnostic, main.retirement.diagnosticsV4(), main.claim.closeDiagnosticsV1(), main.runtime.CloseDiagnosticsV4())
}
