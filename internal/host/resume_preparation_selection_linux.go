//go:build linux

package host

import (
	"bytes"
	"context"
	"errors"
)

// This is a snapshot of the SAME genuine request admission, never an issuer.
// The protected manifest keeps the old selection until the restart writer
// atomically publishes the target with the continuously held accounting leaf.
type resumePreparationSelectionV1 struct {
	before, target                                       Limits
	beforeNeed, beforeRuntime, targetNeed, targetRuntime []byte
}

func captureResumePreparationSelectionV1(ctx context.Context, operation *SessionOperation, target *Manifest) (*resumePreparationSelectionV1, error) {
	before, err := operation.LoadContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := checkAdmittedRestartSelectionV1(operation, before, target); err != nil {
		return nil, err
	}
	beforeRuntime, err := startupRuntimeBytes(before)
	if err != nil {
		return nil, err
	}
	targetRuntime, err := startupRuntimeBytes(target)
	if err != nil {
		return nil, err
	}
	return &resumePreparationSelectionV1{before: before.Limits, target: target.Limits, beforeNeed: bytes.Clone(before.VMNeedV1), beforeRuntime: beforeRuntime, targetNeed: bytes.Clone(target.VMNeedV1), targetRuntime: targetRuntime}, nil
}

func (selection *resumePreparationSelectionV1) checkCurrentV1(operation *SessionOperation, current *Manifest) error {
	if selection == nil || current == nil {
		return resumePreparationDataErrorV1()
	}
	runtime, err := startupRuntimeBytes(current)
	if err != nil {
		return err
	}
	before := current.Limits == selection.before && bytes.Equal(current.VMNeedV1, selection.beforeNeed) && bytes.Equal(runtime, selection.beforeRuntime)
	target := current.Limits == selection.target && bytes.Equal(current.VMNeedV1, selection.targetNeed) && bytes.Equal(runtime, selection.targetRuntime)
	if !before && !target {
		return resumePreparationErrorV1("the held preparation changed both its original and admitted target selection")
	}
	return checkAdmittedRestartCurrentV1(operation, current)
}

func (selection *resumePreparationSelectionV1) checkTargetV1(operation *SessionOperation, target *Manifest) error {
	if selection == nil || target == nil || target.Limits != selection.target || !bytes.Equal(target.VMNeedV1, selection.targetNeed) {
		return resumePreparationDataErrorV1()
	}
	runtime, err := startupRuntimeBytes(target)
	if err != nil || !bytes.Equal(runtime, selection.targetRuntime) {
		return errors.Join(err, resumePreparationDataErrorV1())
	}
	return checkAdmittedRestartCurrentV1(operation, target)
}
