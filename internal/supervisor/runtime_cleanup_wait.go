package supervisor

import (
	"errors"
	"os/exec"

	"github.com/iamlakshya/simstim/internal/jail"
)

// Operational exit status remains in Wait/end diagnostics. It is separated
// from cleanup only after the concrete backend has proved physical closure.
// Generic/context/I/O/close errors remain cleanup failures unchanged.
func withoutOperationalExitV1(err error) error {
	if err == nil {
		return nil
	}
	switch value := err.(type) {
	case *exec.ExitError:
		if value.ProcessState != nil {
			return nil
		}
	case *jail.ExitError:
		return nil
	case interface{ Unwrap() []error }:
		var retained []error
		for _, child := range value.Unwrap() {
			if remaining := withoutOperationalExitV1(child); remaining != nil {
				retained = append(retained, remaining)
			}
		}
		return errors.Join(retained...)
	case interface{ Unwrap() error }:
		if withoutOperationalExitV1(value.Unwrap()) == nil {
			return nil
		}
	}
	return err
}

func (r vmRuntime) rawCleanupWaitErrorV1() error      { return r.Sandbox.Wait().Err }
func (r processSandbox) rawCleanupWaitErrorV1() error { return r.Sandbox.Wait().Err }
func (r noneRuntime) rawCleanupWaitErrorV1() error    { return r.Sandbox.Wait().Err }

func (s *Supervisor) runtimeCleanupWaitV1(runtime sandbox, killErr, waitErr error) error {
	physicalErr := s.checkBackendPhysicalClosureV1(runtime)
	if physicalErr != nil {
		return errors.Join(killErr, waitErr, physicalErr)
	}
	reader, ok := runtime.(interface{ rawCleanupWaitErrorV1() error })
	if !ok {
		return errors.Join(killErr, waitErr)
	}
	return errors.Join(withoutOperationalExitV1(killErr), withoutOperationalExitV1(reader.rawCleanupWaitErrorV1()))
}
