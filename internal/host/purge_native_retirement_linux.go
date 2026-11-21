//go:build linux

package host

import (
	"errors"
	"sync"
)

// This composition adds only existing typed resource-owner retirement to
// the generic per-object native descriptor facts. It issues no Source.
type purgeNativeRetirementsV4 struct {
	nativeStartupRetirementsV1
	mu        sync.Mutex
	lines     map[*StartupLineV1]*purgeLineRetirementV4
	endpoints map[*purgePrivateEndpointV2]struct{}
}

func (owner *purgeNativeRetirementsV4) diagnosticsV4() error {
	diagnostics := owner.nativeStartupRetirementsV1.diagnosticsV4()
	owner.mu.Lock()
	endpoints := make([]*purgePrivateEndpointV2, 0, len(owner.endpoints))
	for endpoint := range owner.endpoints {
		endpoints = append(endpoints, endpoint)
	}
	owner.mu.Unlock()
	for _, endpoint := range endpoints {
		diagnostics = errors.Join(diagnostics, endpoint.CloseDiagnosticsV3())
	}
	return diagnostics
}

func (owner *purgeNativeRetirementsV4) checkAllRetiredV4() error {
	if err := owner.nativeStartupRetirementsV1.checkAllRetiredV4(); err != nil {
		return err
	}
	owner.mu.Lock()
	lines := make([]*StartupLineV1, 0, len(owner.lines))
	for line := range owner.lines {
		lines = append(lines, line)
	}
	endpoints := make([]*purgePrivateEndpointV2, 0, len(owner.endpoints))
	for endpoint := range owner.endpoints {
		endpoints = append(endpoints, endpoint)
	}
	owner.mu.Unlock()
	for _, line := range lines {
		if err := owner.checkStartupLineV4(line); err != nil {
			return err
		}
	}
	for _, endpoint := range endpoints {
		if err := owner.checkEndpointV4(endpoint); err != nil {
			return err
		}
	}
	return nil
}
