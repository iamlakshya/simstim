//go:build linux

package host

import "testing"

func TestLoadedEarlyDeathCleanupCannotCrossActualLoanAdmission(t *testing.T) {
	manager := &nativeUserManagerV1{events: managerStartupEventsV1{loaded: true, dispatched: true}}
	launch := &startupLaunchV1{manager: manager, started: true}
	if !launch.canReconcileLoadedPreloanV1() {
		t.Fatal("the original pre-loan loaded creation lost its cleanup class")
	}
	launch.loanSent = true // The real sender marks this BEFORE its write.
	if launch.canReconcileLoadedPreloanV1() {
		t.Fatal("a lost loan ACK permitted observer-only backend cleanup")
	}
	launch.ready = true
	if launch.canReconcileLoadedPreloanV1() {
		t.Fatal("a committed backend acquired early-death cleanup")
	}
}

func TestLoadedEarlyDeathCleanupNeedsOriginalLoadedDispatch(t *testing.T) {
	for _, test := range []struct{ loaded, dispatched, negative bool }{{}, {loaded: true}, {dispatched: true}, {loaded: true, dispatched: true, negative: true}} {
		launch := &startupLaunchV1{manager: &nativeUserManagerV1{events: managerStartupEventsV1{loaded: test.loaded, dispatched: test.dispatched, negativeReply: test.negative}}, started: true}
		if launch.canReconcileLoadedPreloanV1() {
			t.Fatal("unloaded, undispatched or unrelated negative job acquired native terminal reconciliation")
		}
	}
}
