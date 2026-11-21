//go:build linux

package host

import (
	"context"
	"testing"
)

func TestPurgeAuxiliaryFlagsCannotSupplyStoppedSourceV1(t *testing.T) {
	for _, auxiliary := range []*auxiliaryOperationV1{nil, {}, {settled: true}, {dispatched: true, settled: true}, {closed: true, settled: true}} {
		for _, stopped := range []*StoppedSessionV1{nil, {}, {settled: true}} {
			if err := stopped.checkPurgeAuxiliarySourceV1(context.Background(), &SessionOperation{}, auxiliary); err == nil {
				t.Fatal("copied closure flags supplied native purge authority")
			}
		}
	}
}

func TestPurgeWorkAdapterRefusesDataOnlyStoppedOwnerBeforeInspectionV1(t *testing.T) {
	for _, stopped := range []*StoppedSessionV1{nil, {}, {settled: true}} {
		if err := CheckPurgeUnsavedForOperationV1(context.Background(), &SessionOperation{}, stopped); err == nil {
			t.Fatal("a data-only stopped source reached the guest reader")
		}
	}
}
