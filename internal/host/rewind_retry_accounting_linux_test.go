//go:build linux

package host

import "testing"

func TestRewindRetryDataCannotResetConsumedAccounting(t *testing.T) {
	for _, takenOver := range []bool{false, true} {
		operation := &SessionOperation{id: "parent23"}
		old, next := &ColdResumeV1{operation: operation}, &ColdResumeV1{operation: operation}
		p := &bridgeRewindPreparationV1{operation: operation, cold: old, publication: &runtimePublication{}}
		receipt := &rewindFailedCreationV1{preparation: p, operation: operation, previous: old}
		lease := &resumePreparationLeaseV1{origin: &resumePreparationAccountingOriginV1{rewind: p}, operation: operation, cold: old, transferred: true, takenOver: takenOver}
		p.retry, p.accounting = receipt, lease
		operation.resumePreparation = lease
		if err := p.transferFailedRewindAccountingV1(t.Context(), receipt, old, next, nil); err == nil {
			t.Fatal("a receipt-shaped value transferred native accounting")
		}
		if p.accounting != lease || operation.resumePreparation != lease || !lease.transferred || lease.takenOver != takenOver || lease.cancelled || lease.cold != old {
			t.Fatal("an unproved retry reset or released the actual old accounting pointer")
		}
	}
}
