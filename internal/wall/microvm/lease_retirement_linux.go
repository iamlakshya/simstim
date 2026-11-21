//go:build linux

package microvm

import (
	"errors"
	"fmt"
	"os"
)

func (s *sandbox) closeOwnedDescriptorV2(file *os.File) (bool, error) {
	if s == nil || file == nil {
		return false, fmt.Errorf("the actual descriptor retirement has no original VM owner")
	}
	s.retirementMu.Lock()
	defer s.retirementMu.Unlock()
	if s.retirements == nil {
		s.retirements = make(map[*os.File]*microVMFileRetirementV2)
	}
	owner := s.retirements[file]
	first := owner == nil
	if first {
		owner = &microVMFileRetirementV2{file: file}
		s.retirements[file] = owner
	}
	err := owner.closeV2()
	if first {
		s.retirementDiagnostic = errors.Join(s.retirementDiagnostic, owner.diagnosticV2())
	}
	owner.mu.Lock()
	retired := owner.retired
	owner.mu.Unlock()
	return retired, err
}

// PreparedCloseDiagnosticsV2 preserves actual first-close I/O diagnostics.
// It grants neither descriptor retirement nor native physical cleanup.
func (s *sandbox) PreparedCloseDiagnosticsV2() error {
	s.retirementMu.Lock()
	defer s.retirementMu.Unlock()
	return errors.Join(s.retirementDiagnostic, s.bootFileDiagnostic, s.runtimeLeaseDiagnostic)
}

func (s *sandbox) releaseLease() error {
	if err := s.closeSnapshotDiskV1(); err != nil {
		return err
	}
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	s.mu.Lock()
	lease, unknown := s.lease, s.leaseCloseErr
	s.mu.Unlock()
	if unknown != nil {
		return unknown
	}
	if lease == nil {
		return nil
	}
	// Callers have fenced future Start and joined the original native owner,
	// or this is the constructor's exclusive, never-lent preparation lease.
	retired, err := s.closeOwnedDescriptorV2(lease)
	s.mu.Lock()
	if retired {
		s.lease = nil
	} else if err != nil {
		s.leaseCloseErr = err
	}
	s.mu.Unlock()
	return err
}

func closeVMPreparationFileV1(owner *sandbox, file *os.File) error {
	if owner == nil {
		return file.Close()
	}
	// This actual constructor-local metadata file was never lent; its writer
	// and Sync have returned before this concrete one-shot close is reached.
	retired, err := owner.closeOwnedDescriptorV2(file)
	if err != nil && !retired {
		owner.mu.Lock()
		owner.preparedFiles = append(owner.preparedFiles, file)
		owner.fileCloseErr = errors.Join(owner.fileCloseErr, err)
		owner.mu.Unlock()
	}
	return err
}

func (s *sandbox) preparationFileCloseErrorV1() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fileCloseErr
}
