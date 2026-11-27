//go:build linux

package microvm

import (
	"fmt"
	"github.com/iamlakshya/simstim/internal/supply"
	"os"
	"sync"
)

// This owns one ACTUAL file, not an error-derived replacement. The first
// concrete Close runs once. No raw close, reopened path or fd-number retry
// may touch a descriptor that the kernel could already have reused.
type microVMFileRetirementV2 struct {
	file                *os.File
	closeMu             sync.Mutex
	mu                  sync.Mutex
	borrowers           sync.WaitGroup
	closing             bool
	attempted, retired  bool
	diagnostic, unknown error
}

func (owner *microVMFileRetirementV2) borrowV2() (*os.File, func(), error) {
	if owner == nil {
		return nil, nil, fmt.Errorf("the actual VM file owner is missing")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closing || owner.attempted || owner.file == nil {
		return nil, nil, fmt.Errorf("the actual VM file owner is closing")
	}
	owner.borrowers.Add(1)
	var once sync.Once
	return owner.file, func() { once.Do(owner.borrowers.Done) }, nil
}

func (owner *microVMFileRetirementV2) closeV2() error {
	if owner == nil || owner.file == nil {
		return nil
	}
	owner.closeMu.Lock()
	defer owner.closeMu.Unlock()
	owner.mu.Lock()
	owner.closing = true
	owner.mu.Unlock()
	owner.borrowers.Wait()
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.attempted {
		if owner.retired {
			return nil
		}
		return owner.unknown
	}
	owner.attempted = true
	facts := supply.CloseFileOnceV1(owner.file)
	owner.retired, owner.diagnostic, owner.unknown = facts.Retired, facts.Diagnostic, facts.Unknown
	if facts.Unknown != nil {
		return facts.Unknown
	}
	return facts.Diagnostic
}

func (owner *microVMFileRetirementV2) diagnosticV2() error {
	if owner == nil {
		return nil
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	return owner.diagnostic
}

// Test/diagnostic observation only. The shared operation checks its own
// actual locked thread again; this observation grants no close inference.
func actualUnfilteredCloseThreadV2() error { return supply.CheckFileCloseThreadV1() }
