//go:build linux

package host

import (
	"context"
	"errors"
	"testing"
)

func TestStoppedColdHandoffRejectsDataSubstitutionAndRepeat(t *testing.T) {
	operation := &SessionOperation{id: "parent23"}
	for _, owner := range []*StoppedSessionV1{nil, {}, {operation: operation, settled: true}, {operation: operation, settled: true, holderHanded: true}} {
		cold := &ColdResumeV1{operation: operation, bound: true, vacancy: &auxiliaryVacancyV1{operation: operation, stopped: owner}}
		operation.nativeColdResume = cold
		if handled, err := consumeStoppedColdHoldersV1(t.Context(), operation, cold); owner != nil && (!handled || err == nil) {
			t.Fatal("data substituted for the actual original holder copies")
		}
		if owner == nil {
			continue
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := consumeStoppedColdHoldersV1(ctx, operation, cold); err == nil {
			t.Fatal("cancelled handoff accepted an unproved source")
		}
	}
}

func TestColdCloseRetainsDescriptorFailureOnSecondClose(t *testing.T) {
	failure := errors.New("original descriptor close failed")
	cold := &ColdResumeV1{closed: true, closeErr: failure}
	for i := 0; i < 2; i++ {
		if err := cold.Close(); !errors.Is(err, failure) {
			t.Fatalf("close %d lost its original failure: %v", i, err)
		}
	}
}

func TestRewindRetryRequiresActualBirthAndCapturedWholeGroup(t *testing.T) {
	operation := &SessionOperation{id: "parent23"}
	p := &bridgeRewindPreparationV1{operation: operation, execution: t.Context(), consumed: true}
	p.active.Store(true)
	receipt := &rewindFailedCreationV1{operation: operation, preparation: p, boot: BootID(), vacancy: &auxiliaryVacancyV1{operation: operation}}
	p.retry = receipt
	if err := receipt.checkV1(t.Context(), operation); err == nil {
		t.Fatal("status/absence DATA created a failed-creation retry source")
	}
}
