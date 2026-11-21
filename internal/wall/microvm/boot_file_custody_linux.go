//go:build linux

package microvm

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/iamlakshya/simstim/internal/supply"
)

func closeMicroVMFileV2(actual **os.File, states map[*os.File]*microVMFileRetirementV2, diagnostic *error) error {
	if actual == nil || *actual == nil {
		return nil
	}
	file := *actual
	owner := states[file]
	if owner == nil {
		owner = &microVMFileRetirementV2{file: file}
		states[file] = owner
	}
	first := !owner.attempted
	err := owner.closeV2()
	if first && owner.diagnosticV2() != nil {
		*diagnostic = errors.Join(*diagnostic, owner.diagnosticV2())
	}
	if err != nil {
		return err
	}
	*actual = nil
	return nil
}

func closeMicroVMRuntimeOwnersV2(owners *[]*supply.VerifiedRuntime, files *[]*os.File, states map[*os.File]*microVMFileRetirementV2, diagnostic *error) error {
	for {
		if len(*files) > 0 {
			if err := closeMicroVMFileV2(&(*files)[0], states, diagnostic); err != nil {
				return err
			}
			*files = (*files)[1:]
			continue
		}
		if len(*owners) == 0 {
			return nil
		}
		owner := (*owners)[0]
		actual, err := owner.TakeFileForCloseV1()
		if err != nil {
			return err
		}
		if actual != nil {
			*files = append(*files, actual)
		} // Adopt BEFORE removing its original higher owner.
		*owners = (*owners)[1:]
	}
}

var errBootFilesBorrowedV2 = errors.New("the original native VM still borrows its boot files")

// One sandbox retains one ACTUAL factory result. No error, saved row or Spec
// reconstructs this object. Unknown native closure keeps the same custody.
type bootFileCustodyV2 struct {
	mu      sync.Mutex
	files   *bootFiles
	command ownedVMProcess
}

func (s *sandbox) adoptBootFilesV2(files *bootFiles) error {
	if files == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bootCustody != nil && s.bootCustody.files != files {
		return fmt.Errorf("the sandbox still owns another actual boot-file cleanup; Try: finish that original cleanup first")
	}
	if s.bootCustody == nil {
		s.bootCustody = &bootFileCustodyV2{files: files}
	}
	return nil
}

func (s *sandbox) attachBootFileCommandV2(command ownedVMProcess) error {
	s.mu.Lock()
	custody := s.bootCustody
	s.mu.Unlock()
	if custody == nil || command == nil {
		return fmt.Errorf("the original VM command has no actual boot-file owner")
	}
	custody.mu.Lock()
	defer custody.mu.Unlock()
	if custody.command != nil {
		return fmt.Errorf("the original boot-file owner already lent its files to a native command")
	}
	custody.command = command
	return nil
}

func (s *sandbox) releaseBootFilesV2() error {
	s.mu.Lock()
	custody := s.bootCustody
	s.mu.Unlock()
	if custody == nil {
		return nil
	}
	custody.mu.Lock()
	defer custody.mu.Unlock()
	if custody.command != nil && !custody.command.PhysicalExitV1() {
		return fmt.Errorf("%w; Try: retain its actual command and join physical cleanup", errBootFilesBorrowedV2)
	}
	if err := custody.files.close(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.bootCustody == custody {
		s.bootCustody = nil
	}
	s.mu.Unlock()
	return nil
}

// Keep the VM lock below every data/native borrower. Destroy uses the first
// helper before deletion and retains that lock through the destructive step.
func (s *sandbox) releaseBootBorrowersV2() error {
	if err := s.releaseBootFilesV2(); err != nil {
		return err
	}
	return s.releaseAgentsLease()
}
func (s *sandbox) releaseBootStackV2() error {
	if err := s.releaseBootBorrowersV2(); err != nil {
		return err
	}
	return s.releaseLease()
}

func (s *sandbox) finishBootFilesAtReturnV2(returned *error) {
	err := s.releaseBootFilesV2()
	if errors.Is(err, errBootFilesBorrowedV2) {
		return
	}
	*returned = errors.Join(*returned, err)
	if err != nil {
		s.mu.Lock()
		s.exit.Err = errors.Join(s.exit.Err, err)
		s.mu.Unlock()
	}
}
