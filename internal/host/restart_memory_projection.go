package host

import (
	"bytes"
	"encoding/json"
	"reflect"

	"github.com/iamlakshya/simstim/internal/supply"
)

func applyAdmittedRestartMemoryV1(operation *SessionOperation, m *Manifest) error {
	if operation == nil || m == nil || !operation.Valid(m.ID) {
		return sessionLeaseError("restart memory has no original current operation")
	}
	operation.gate.Lock()
	defer operation.gate.Unlock()
	receipt := operation.restartMemory
	if receipt == nil || receipt.operation != operation || receipt.used || !operation.active.Load() {
		return sessionLeaseError("the restart has no unconsumed original memory admission")
	}
	// The ordinary resume body already records the requested intent before
	// this projection. It may change MemoryAsked, but cannot replace the
	// original cap, Need, runtime or agents selection ahead of this owner.
	original := *m
	original.Limits.MemoryAsked = receipt.before.MemoryAsked
	if checkRestartReceiptSelectionV1(receipt, &original, false) != nil {
		if err := checkRestartReceiptSelectionV1(receipt, m, true); err != nil {
			return err
		}
	}
	m.Limits = receipt.after
	m.VMNeedV1 = bytes.Clone(receipt.nextNeed)
	m.Agents = receipt.nextAgents
	var runtime *supply.Manifest
	if err := json.Unmarshal(receipt.nextRuntime, &runtime); err != nil {
		return err
	}
	m.RuntimeManifest = runtime
	return nil
}

// These checks validate a retained admission. They never issue or consume one.
func checkRestartReceiptSelectionV1(r *restartMemoryAdmissionV1, m *Manifest, target bool) error {
	if r == nil || m == nil {
		return sessionLeaseError("the original restart selection is absent")
	}
	limits, need, runtime, agents := r.before, r.need, r.runtime, r.beforeAgents
	if target {
		limits, need, runtime, agents = r.after, r.nextNeed, r.nextRuntime, r.nextAgents
	}
	encoded, err := json.Marshal(m.RuntimeManifest)
	if err != nil || !reflect.DeepEqual(limits, m.Limits) || !bytes.Equal(need, m.VMNeedV1) || !bytes.Equal(runtime, encoded) || m.Agents != agents {
		return sessionLeaseError("the restart selection differs from its original admitted source")
	}
	return nil
}
func checkAdmittedRestartSelectionV1(operation *SessionOperation, before, target *Manifest) error {
	if operation == nil || !operation.Valid(operation.Primary()) {
		return sessionLeaseError("restart selection has no original operation")
	}
	operation.gate.Lock()
	defer operation.gate.Unlock()
	r := operation.restartMemory
	if r == nil || r.operation != operation || !operation.active.Load() {
		return sessionLeaseError("restart selection has no original admission")
	}
	if err := checkRestartReceiptSelectionV1(r, before, false); err != nil {
		return err
	}
	return checkRestartReceiptSelectionV1(r, target, true)
}
func checkAdmittedRestartCurrentV1(operation *SessionOperation, current *Manifest) error {
	if operation == nil || !operation.Valid(operation.Primary()) {
		return sessionLeaseError("restart selection has no original operation")
	}
	operation.gate.Lock()
	defer operation.gate.Unlock()
	r := operation.restartMemory
	if r == nil || r.operation != operation || !operation.active.Load() {
		return sessionLeaseError("restart selection has no original admission")
	}
	if checkRestartReceiptSelectionV1(r, current, false) == nil {
		return nil
	}
	return checkRestartReceiptSelectionV1(r, current, true)
}

// A cold vacancy may follow only its exact original or admitted target bytes.
func checkColdRestartSelectionV1(operation *SessionOperation, need, runtime []byte, current *Manifest) error {
	encoded, err := startupRuntimeBytes(current)
	if err != nil {
		return err
	}
	if bytes.Equal(need, current.VMNeedV1) && bytes.Equal(runtime, encoded) {
		return nil
	}
	if operation == nil {
		return sessionLeaseError("the cold source has no original operation")
	}
	operation.gate.Lock()
	r := operation.restartMemory
	same := r != nil && bytes.Equal(r.need, need) && bytes.Equal(r.runtime, runtime)
	operation.gate.Unlock()
	if !same {
		return sessionLeaseError("the cold source differs from the original restart admission")
	}
	return checkAdmittedRestartCurrentV1(operation, current)
}
